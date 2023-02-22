// Package cli provides easy-to-use commands to manage, monitor, and utilize AIS clusters.
// This file contains util functions and types.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmd/cli/teb"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/ios"
	"github.com/NVIDIA/aistore/stats"
	"github.com/NVIDIA/aistore/sys"
	"github.com/urfave/cli"
	"golang.org/x/sync/errgroup"
)

// NOTE: target's metric names & kinds
func getMetricNames(c *cli.Context) (cos.StrKVs, error) {
	smap, err := getClusterMap(c)
	if err != nil {
		return nil, err
	}
	if smap.CountActiveTs() == 0 {
		return nil, nil
	}
	tsi, err := smap.GetRandTarget()
	if err != nil {
		return nil, err
	}
	return api.GetMetricNames(apiBP, tsi)
}

//
// teb.StStMap
//

func fillNodeStatusMap(c *cli.Context, daeType string) (smap *cluster.Smap, tstatusMap, pstatusMap teb.StStMap, err error) {
	if smap, err = getClusterMap(c); err != nil {
		return
	}
	var (
		wg         cos.WG
		mu         = &sync.Mutex{}
		pcnt, tcnt = smap.CountProxies(), smap.CountTargets()
	)
	switch daeType {
	case apc.Target:
		wg = cos.NewLimitedWaitGroup(sys.NumCPU(), tcnt)
		tstatusMap = make(teb.StStMap, tcnt)
		daeStatus(smap.Tmap, tstatusMap, wg, mu)
	case apc.Proxy:
		wg = cos.NewLimitedWaitGroup(sys.NumCPU(), pcnt)
		pstatusMap = make(teb.StStMap, pcnt)
		daeStatus(smap.Pmap, pstatusMap, wg, mu)
	default:
		wg = cos.NewLimitedWaitGroup(sys.NumCPU(), pcnt+tcnt)
		tstatusMap = make(teb.StStMap, tcnt)
		pstatusMap = make(teb.StStMap, pcnt)
		daeStatus(smap.Tmap, tstatusMap, wg, mu)
		daeStatus(smap.Pmap, pstatusMap, wg, mu)
	}

	wg.Wait()
	return
}

func daeStatus(nodeMap cluster.NodeMap, out teb.StStMap, wg cos.WG, mu *sync.Mutex) {
	for _, si := range nodeMap {
		wg.Add(1)
		go func(si *cluster.Snode) {
			_status(si, mu, out)
			wg.Done()
		}(si)
	}
}

func _status(node *cluster.Snode, mu *sync.Mutex, out teb.StStMap) {
	daeStatus, err := api.GetStatsAndStatus(apiBP, node)
	if err != nil {
		daeStatus = &stats.NodeStatus{}
		daeStatus.Snode = node
		if herr, ok := err.(*cmn.ErrHTTP); ok {
			daeStatus.Status = herr.TypeCode
		} else if strings.HasPrefix(err.Error(), "errNodeNotFound") {
			daeStatus.Status = "[errNodeNotFound]"
		} else {
			daeStatus.Status = "[" + err.Error() + "]"
		}
	} else if daeStatus.Status == "" {
		daeStatus.Status = teb.NodeOnline
		switch {
		case node.Flags.IsSet(cluster.NodeFlagMaint):
			daeStatus.Status = apc.NodeMaintenance
		case node.Flags.IsSet(cluster.NodeFlagDecomm):
			daeStatus.Status = apc.NodeDecommission
		}
	}

	mu.Lock()
	out[node.ID()] = daeStatus
	mu.Unlock()
}

//
// throughput
//

// throughput as F(stats.DaemonStats)
func _daeBps(node *cluster.Snode, metrics cos.StrKVs, statsBegin *stats.Node, averageOver time.Duration) error {
	time.Sleep(averageOver)

	statsEnd, err := api.GetDaemonStats(apiBP, node)
	if err != nil {
		return err
	}
	seconds := cos.MaxI64(int64(averageOver.Seconds()), 1)
	debug.Assert(seconds > 1)
	for k, v := range statsBegin.Tracker {
		vend := statsEnd.Tracker[k]
		if metrics[k] == stats.KindThroughput {
			if v.Value > 0 {
				throughput := (vend.Value - v.Value) / seconds
				v.Value = throughput
			}
		} else {
			v.Value = vend.Value // more recent
		}
		statsBegin.Tracker[k] = v
	}
	return nil
}

// troughput as F(stats.ClusterStats)
func _cluStatsBps(metrics cos.StrKVs, statsBegin stats.Cluster, averageOver time.Duration) error {
	time.Sleep(averageOver)

	statsEnd, err := api.GetClusterStats(apiBP)
	if err != nil {
		return err
	}
	seconds := cos.MaxI64(int64(averageOver.Seconds()), 1)
	debug.Assert(seconds > 1)
	for tid, begin := range statsBegin.Target {
		end := statsEnd.Target[tid]
		if begin == nil || end == nil {
			return fmt.Errorf("%s seems to be offline", cluster.Tname(tid))
		}
		for name, v := range begin.Tracker {
			vend := end.Tracker[name]
			// (unlike stats.KindComputedThroughput)
			if metrics[name] == stats.KindThroughput {
				if v.Value > 0 {
					throughput := (vend.Value - v.Value) / seconds
					v.Value = throughput
				}
			} else {
				v.Value = vend.Value // more timely
			}
			begin.Tracker[name] = v
		}
	}
	return nil
}

func _cluStatusBeginEnd(c *cli.Context, ini teb.StStMap, sleep time.Duration) (b, e teb.StStMap, err error) {
	b = ini
	if b == nil {
		// begin stats
		if _, b, _, err = fillNodeStatusMap(c, apc.Target); err != nil {
			return nil, nil, err
		}
	}

	time.Sleep(sleep)

	// post-interval (end) stats
	_, e, _, err = fillNodeStatusMap(c, apc.Target)
	return
}

////////////
// dstats //
////////////

type (
	dstats struct {
		tid   string
		stats ios.AllDiskStats
	}
	dstatsCtx struct {
		tid string
		ch  chan dstats
	}
)

func (ctx *dstatsCtx) get() error {
	diskStats, err := api.GetDiskStats(apiBP, ctx.tid)
	if err != nil {
		return err
	}
	ctx.ch <- dstats{stats: diskStats, tid: ctx.tid}
	return nil
}

func getDiskStats(smap *cluster.Smap, tid string) ([]teb.DiskStatsHelper, error) {
	var (
		targets = smap.Tmap
		l       = smap.CountActiveTs()
	)
	if tid != "" {
		tsi := smap.GetNode(tid)
		if tsi.InMaintOrDecomm() {
			return nil, fmt.Errorf("target %s is unaivailable at this point", tsi.StringEx())
		}
		targets = cluster.NodeMap{tid: tsi}
		l = 1
	}
	allStats := make([]teb.DiskStatsHelper, 0, l)
	ch := make(chan dstats, l)

	wg, _ := errgroup.WithContext(context.Background())
	for tid, tsi := range targets {
		if tsi.InMaintOrDecomm() {
			continue
		}
		ctx := &dstatsCtx{ch: ch, tid: tid}
		wg.Go(ctx.get)
	}

	err := wg.Wait()
	close(ch)
	if err != nil {
		return nil, err
	}
	for diskStats := range ch {
		for diskName, diskStat := range diskStats.stats {
			allStats = append(allStats,
				teb.DiskStatsHelper{
					TargetID: diskStats.tid,
					DiskName: diskName,
					Stat:     diskStat,
				})
		}
	}

	sort.Slice(allStats, func(i, j int) bool {
		if allStats[i].TargetID != allStats[j].TargetID {
			return allStats[i].TargetID < allStats[j].TargetID
		}
		if allStats[i].DiskName != allStats[j].DiskName {
			return allStats[i].DiskName < allStats[j].DiskName
		}
		return allStats[i].Stat.Util > allStats[j].Stat.Util
	})

	return allStats, nil
}