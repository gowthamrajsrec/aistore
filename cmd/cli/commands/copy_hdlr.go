// Package commands provides the set of CLI commands used to communicate with the AIS cluster.
// This file handles commands that copy buckets and objects in the cluster.
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package commands

import (
	"fmt"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/urfave/cli"
)

var (
	copyCmdsFlags = map[string][]cli.Flag{
		subcmdCopyBucket: {
			cpBckDryRunFlag,
			cpBckPrefixFlag,
		},
	}

	copyCmds = []cli.Command{
		{
			Name:  commandCopy,
			Usage: "copy buckets and objects in the cluster",
			Subcommands: []cli.Command{
				{
					Name:         subcmdCopyBucket,
					Usage:        "copy ais buckets",
					ArgsUsage:    bucketOldNewArgument,
					Flags:        copyCmdsFlags[subcmdCopyBucket],
					Action:       copyBucketHandler,
					BashComplete: oldAndNewBucketCompletions([]cli.BashCompleteFunc{}, false /* separator */, cmn.ProviderAIS),
				},
			},
		},
	}
)

func copyBucketHandler(c *cli.Context) (err error) {
	bucketName, newBucketName, err := getOldNewBucketName(c)
	if err != nil {
		return err
	}
	fromBck, objName, err := cmn.ParseBckObjectURI(bucketName)
	if err != nil {
		return err
	}
	toBck, newObjName, err := cmn.ParseBckObjectURI(newBucketName)
	if err != nil {
		return err
	}
	if fromBck.IsCloud() || toBck.IsCloud() {
		return fmt.Errorf("copying of cloud buckets not supported")
	}
	if fromBck.IsRemoteAIS() || toBck.IsRemoteAIS() {
		return fmt.Errorf("copying of remote ais buckets not supported")
	}
	if objName != "" {
		return objectNameArgumentNotSupported(c, objName)
	}
	if newObjName != "" {
		return objectNameArgumentNotSupported(c, objName)
	}

	fromBck.Provider, toBck.Provider = cmn.ProviderAIS, cmn.ProviderAIS
	msg := &cmn.CopyBckMsg{
		Prefix: parseStrFlag(c, cpBckPrefixFlag),
		DryRun: flagIsSet(c, cpBckDryRunFlag),
	}

	if msg.DryRun {
		// TODO: once IC is integrated with copy-bck stats, show something more relevant, like stream of object names
		// with destination which they would have been copied to. Then additionally, make output consistent with etl
		// dry-run output.
		fmt.Fprintln(c.App.Writer, dryRunHeader+" "+dryRunExplanation)
	}

	return copyBucket(c, fromBck, toBck, msg)
}
