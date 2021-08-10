// Package xs contains eXtended actions (xactions) except storage services
// (mirror, ec) and extensions (downloader, lru).
/*
 * Copyright (c) 2021, NVIDIA CORPORATION. All rights reserved.
 */
package xs

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/3rdparty/atomic"
	"github.com/NVIDIA/aistore/3rdparty/glog"
	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/transport"
	"github.com/NVIDIA/aistore/transport/bundle"
	"github.com/NVIDIA/aistore/xaction"
	"github.com/NVIDIA/aistore/xreg"
)

type (
	tcoFactory struct {
		xreg.RenewBase
		xact *XactTransCopyObjs
		kind string
		args *xreg.TransCpyObjsArgs
	}
	XactTransCopyObjs struct {
		xaction.DemandBase
		t       cluster.Target
		args    *xreg.TransCpyObjsArgs
		workCh  chan *cmn.TransCpyListRangeMsg
		config  *cmn.Config
		dm      *bundle.DataMover
		pending struct { // TODO -- FIXME: remove
			sync.RWMutex
			m map[string]*tcowi
		}
	}
	tcowi struct {
		r   *XactTransCopyObjs
		msg *cmn.TransCpyListRangeMsg
		// finishing
		refc atomic.Int32
	}
)

// interface guard
var (
	_ cluster.Xact   = (*XactTransCopyObjs)(nil)
	_ xreg.Renewable = (*tcoFactory)(nil)
)

////////////////
// tcoFactory //
////////////////

func (p *tcoFactory) New(args xreg.Args, fromBck *cluster.Bck) xreg.Renewable {
	np := &tcoFactory{RenewBase: xreg.RenewBase{Args: args, Bck: fromBck}, kind: p.kind}
	np.args = args.Custom.(*xreg.TransCpyObjsArgs)
	return np
}

func (p *tcoFactory) Start() error {
	var (
		config      = cmn.GCO.Get()
		totallyIdle = config.Timeout.SendFile.D()
		likelyIdle  = config.Timeout.MaxKeepalive.D()
		workCh      = make(chan *cmn.TransCpyListRangeMsg, maxNumInParallel)
	)
	r := &XactTransCopyObjs{t: p.T, args: p.args, workCh: workCh, config: config}
	r.pending.m = make(map[string]*tcowi, maxNumInParallel)
	p.xact = r
	r.DemandBase.Init(p.UUID(), p.Kind(), p.Bck, totallyIdle, likelyIdle)
	if err := p.newDM(p.UUID()); err != nil {
		return err
	}
	r.dm.SetXact(r)
	r.dm.Open()

	xaction.GoRunW(r)
	return nil
}

func (p *tcoFactory) newDM(uuid string) error {
	var (
		trname  = "transcpy-" + "-" + uuid
		sizePDU int32
	)
	if p.kind == cmn.ActETLBck {
		sizePDU = memsys.DefaultBufSize
	}
	dmExtra := bundle.Extra{Multiplier: 1, SizePDU: sizePDU}
	dm, err := bundle.NewDataMover(p.T, trname, p.xact.recv, cluster.RegularPut, dmExtra)
	if err != nil {
		return err
	}
	if err := dm.RegRecv(); err != nil {
		return err
	}
	p.xact.dm = dm
	return nil
}

func (p *tcoFactory) Kind() string      { return p.kind }
func (p *tcoFactory) Get() cluster.Xact { return p.xact }

func (p *tcoFactory) WhenPrevIsRunning(xprev xreg.Renewable) (xreg.WPR, error) {
	debug.Assertf(false, "%s vs %s", p.Str(p.Kind()), xprev) // xreg.usePrev() must've returned true
	return xreg.WprUse, nil
}

///////////////////////
// XactTransCopyObjs //
///////////////////////

func (r *XactTransCopyObjs) Do(msg *cmn.TransCpyListRangeMsg) {
	r.IncPending()
	r.workCh <- msg
}

func (r *XactTransCopyObjs) Run(wg *sync.WaitGroup) {
	var err error
	glog.Infoln(r.String())
	wg.Done()
	for {
		select {
		case msg := <-r.workCh:
			var (
				smap    = r.t.Sowner().Get()
				lrit    = &lriterator{}
				wi      = &tcowi{r: r, msg: msg}
				freeLOM = false // not delegating
			)
			wi.refc.Store(int32(smap.CountTargets() - 1)) // TODO -- FIXME: later
			lrit.init(r, r.t, &msg.ListRangeMsg, freeLOM)
			if msg.IsList() {
				err = lrit.iterateList(wi, smap)
			} else {
				err = lrit.iterateRange(wi, smap)
			}
			if r.Aborted() || err != nil {
				goto fin
			}
			// TODO -- FIXME: broadcast doneSendingOpcode
			r.DecPending()
		case <-r.IdleTimer():
			goto fin
		case <-r.ChanAbort():
			goto fin
		}
	}
fin:
	r.DemandBase.Stop()
	r.dm.Close(err)
	go func() {
		time.Sleep(delayUnregRecv)
		r.dm.UnregRecv()
	}()
	r.Finish(err)
}

func (wi *tcowi) do(lom *cluster.LOM, lri *lriterator) (err error) {
	var size int64
	objNameTo := wi.msg.ToName(lom.ObjName)
	buf, slab := lri.t.MMSA().Alloc()
	params := &cluster.CopyObjectParams{}
	{
		params.BckTo = wi.r.args.BckTo
		params.ObjNameTo = objNameTo
		params.DM = wi.r.dm
		params.Buf = buf
		params.DP = wi.r.args.DP
		params.DryRun = wi.msg.DryRun
	}
	size, err = lri.t.CopyObject(lom, params, false /*localOnly*/)
	slab.Free(buf)
	if err != nil {
		if cos.IsErrOOS(err) {
			what := fmt.Sprintf("%s(%q)", wi.r.Kind(), wi.r.ID())
			err = cmn.NewAbortedError(what, err.Error())
		}
		return
	}
	wi.r.ObjectsInc()
	wi.r.BytesAdd(size)
	return
}

func (r *XactTransCopyObjs) recv(hdr transport.ObjHdr, objReader io.Reader, err error) {
	defer transport.FreeRecv(objReader)
	if err != nil && !cos.IsEOF(err) {
		glog.Error(err)
		return
	}
	if hdr.Opcode == doneSendingOpcode {
		// refc := r.refc.Dec() // TODO -- FIXME: later
		return
	}
	debug.Assert(hdr.Opcode == 0)

	defer cos.DrainReader(objReader)
	lom := cluster.AllocLOM(hdr.ObjName)
	defer cluster.FreeLOM(lom)
	if err := lom.Init(hdr.Bck); err != nil {
		glog.Error(err)
		return
	}
	lom.CopyAttrs(&hdr.ObjAttrs, true /*skip cksum*/)
	params := cluster.PutObjectParams{
		Tag:    fs.WorkfilePut,
		Reader: io.NopCloser(objReader),
		// Transaction is used only by CopyBucket and ETL. In both cases new objects
		// are created at the destination. Setting `RegularPut` type informs `c.t.PutObject`
		// that it must PUT the object to the Cloud as well after the local data are
		// finalized
		RecvType: cluster.RegularPut,
		Cksum:    hdr.ObjAttrs.Cksum,
		Started:  time.Now(),
	}
	if err := r.t.PutObject(lom, params); err != nil {
		glog.Error(err)
	}
}

func (r *XactTransCopyObjs) String() string {
	return fmt.Sprintf("%s <= %s", r.XactBase.String(), r.args.BckFrom)
}

// limited pre-run abort
func (r *XactTransCopyObjs) TxnAbort() {
	err := cmn.NewAbortedError(r.String())
	if r.dm.IsOpen() {
		r.dm.Close(err)
	}
	r.dm.UnregRecv()
	r.XactBase.Finish(err)
}