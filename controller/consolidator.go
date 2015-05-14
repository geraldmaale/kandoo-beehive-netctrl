package controller

import (
	"github.com/kandoo/beehive/Godeps/_workspace/src/github.com/golang/glog"

	bh "github.com/kandoo/beehive"
	"github.com/kandoo/beehive-netctrl/nom"
	"github.com/kandoo/beehive/gob"
)

const (
	MaxPings = 3
)

type Consolidator struct{}

func (c Consolidator) Rcv(msg bh.Msg, ctx bh.RcvContext) error {
	res := msg.Data().(nom.FlowStatsQueryResult)
	var nf nodeFlows
	ctx.Dict(flowsDict).GetGob(string(res.Node), &nf)
	var nt nodeTriggers
	ctx.Dict(triggersDict).GetGob(string(res.Node), &nt)
	found := false
	matchedFlows := make(map[int]struct{})
	for _, stat := range res.Stats {
		for i := range nf.Flows {
			if nf.Flows[i].FlowEntry.Match.Equals(stat.Match) {
				found = true
				matchedFlows[i] = struct{}{}
				nf.Flows[i].updateStats(stat)
			}
		}
		if !found {
			nf.Flows = append(nf.Flows, flow{
				FlowEntry: nom.FlowEntry{
					Match: stat.Match,
				},
				Duration: stat.Duration,
				Packets:  stat.Packets,
				Bytes:    stat.Bytes,
			})
			// TODO(soheil): emit flow entry here.
		}

		for _, t := range nt.Triggers {
			if t.Fired(stat) {
				triggered := newTriggered(t, stat.Duration, stat.BW())
				sub := t.Subscriber
				if !sub.IsNil() {
					ctx.SendToCell(triggered, sub.App, sub.Cell())
				}
			}
		}
	}

	count := 0
	for i, f := range nf.Flows {
		if _, ok := matchedFlows[i]; ok {
			continue
		}

		i -= count
		nf.Flows = append(nf.Flows[:i], nf.Flows[i+1:]...)
		count++
		del := nom.FlowEntryDeleted{
			Flow: f.FlowEntry,
		}
		ctx.Emit(del)
		for _, sub := range f.FlowSubscribers {
			if !sub.IsNil() {
				ctx.SendToCell(del, sub.App, sub.Cell())
			}
		}
	}

	return ctx.Dict(flowsDict).PutGob(string(res.Node), &nf)
}

func (c Consolidator) Map(msg bh.Msg, ctx bh.MapContext) bh.MappedCells {
	return nodeDriversMap(msg.Data().(nom.FlowStatsQueryResult).Node)
}

type poll struct{}

type Poller struct{}

func (p Poller) Rcv(msg bh.Msg, ctx bh.RcvContext) error {
	dict := ctx.Dict(driversDict)
	dict.ForEach(func(k string, v []byte) {
		node := nom.UID(k)
		query := nom.FlowStatsQuery{
			Node: node,
		}
		sendToMaster(query, node, ctx)

		nd := nodeDrivers{}
		if err := gob.Decode(&nd, v); err != nil {
			glog.Warningf("error in decoding drivers: %v", err)
			return
		}

		for i := range nd.Drivers {
			// TODO(soheil): remove the hardcoded value.
			if nd.Drivers[i].OutPings > MaxPings {
				ctx.SendToBee(nom.NodeDisconnected{
					Node:   nom.Node{ID: nom.NodeID(node)},
					Driver: nd.Drivers[i].Driver,
				}, ctx.ID())
				continue
			}

			ctx.SendToBee(nom.Ping{}, nd.Drivers[i].BeeID)
			nd.Drivers[i].OutPings++
		}

		if err := dict.PutGob(k, nd); err != nil {
			glog.Warningf("error in encoding drivers: %v", err)
		}
	})
	return nil
}

func (p Poller) Map(msg bh.Msg, ctx bh.MapContext) bh.MappedCells {
	return bh.MappedCells{}
}
