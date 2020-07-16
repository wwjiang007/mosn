package mirror

import (
	"context"
	"net"

	"mosn.io/api"
	mosnctx "mosn.io/mosn/pkg/context"
	"mosn.io/mosn/pkg/log"
	"mosn.io/mosn/pkg/protocol"
	"mosn.io/mosn/pkg/protocol/xprotocol"
	"mosn.io/mosn/pkg/types"
	"mosn.io/mosn/pkg/upstream/cluster"
	"mosn.io/pkg/buffer"
)

type mirror struct {
	amplification  int
	receiveHandler api.StreamReceiverFilterHandler

	ctx      context.Context
	headers  api.HeaderMap
	data     buffer.IoBuffer
	trailers api.HeaderMap

	clusterName string
	cluster     types.ClusterInfo

	sender types.StreamSender
	host   types.Host
}

func (m *mirror) SetReceiveFilterHandler(handler api.StreamReceiverFilterHandler) {
	m.receiveHandler = handler
}

func (m *mirror) OnReceive(ctx context.Context, headers api.HeaderMap, buf buffer.IoBuffer, trailers api.HeaderMap) api.StreamFilterStatus {
	// router := m.receiveHandler.Route().RouteRule().Policy().HashPolicy()

	// TODO if need mirror

	go func() {
		clusterManager := cluster.NewClusterManagerSingleton(nil, nil)

		clusterName := "mirror"

		m.ctx = mosnctx.WithValue(mosnctx.Clone(ctx), types.ContextKeyBufferPoolCtx, nil)
		if headers != nil {
			if _, ok := headers.(xprotocol.XFrame); ok {
				m.headers = headers
			} else {
				m.headers = headers.Clone()
			}
		}
		if buf != nil {
			m.data = buf.Clone()
		}
		if trailers != nil {
			m.trailers = trailers.Clone()
		}

		snap := clusterManager.GetClusterSnapshot(ctx, clusterName)
		m.cluster = snap.ClusterInfo()
		m.clusterName = clusterName

		currentProtocol := m.getUpstreamProtocol()

		for i := 0; i < m.amplification; i++ {
			connPool := clusterManager.ConnPoolForCluster(m, snap, currentProtocol)
			connPool.NewStream(m.ctx, nil, m)
		}
	}()
	return api.StreamFilterContinue
}

func (m *mirror) OnDestroy() {}

func (m *mirror) convertProtocol() (dp, up types.ProtocolName) {
	dp = m.getDownStreamProtocol()
	up = m.getUpstreamProtocol()
	return
}

func (m *mirror) getDownStreamProtocol() (prot types.ProtocolName) {
	if dp, ok := mosnctx.Get(m.ctx, types.ContextKeyConfigDownStreamProtocol).(string); ok {
		return types.ProtocolName(dp)
	}
	return m.receiveHandler.RequestInfo().Protocol()
}

func (m *mirror) getUpstreamProtocol() (currentProtocol types.ProtocolName) {
	configProtocol, ok := mosnctx.Get(m.ctx, types.ContextKeyConfigUpStreamProtocol).(string)
	if !ok {
		configProtocol = string(protocol.Xprotocol)
	}

	if m.receiveHandler.Route() != nil && m.receiveHandler.Route().RouteRule() != nil && m.receiveHandler.Route().RouteRule().UpstreamProtocol() != "" {
		configProtocol = m.receiveHandler.Route().RouteRule().UpstreamProtocol()
	}

	if configProtocol == string(protocol.Auto) {
		currentProtocol = m.getDownStreamProtocol()
	} else {
		currentProtocol = types.ProtocolName(configProtocol)
	}
	return currentProtocol
}

func (m *mirror) MetadataMatchCriteria() api.MetadataMatchCriteria {
	return nil
}

func (m *mirror) DownstreamConnection() net.Conn {
	return m.receiveHandler.Connection().RawConn()
}

func (m *mirror) DownstreamHeaders() types.HeaderMap {
	return m.headers
}

func (m *mirror) DownstreamContext() context.Context {
	return m.ctx
}

func (m *mirror) DownstreamCluster() types.ClusterInfo {
	return m.cluster
}

func (m *mirror) DownstreamRoute() api.Route {
	return m.receiveHandler.Route()
}

func (m *mirror) OnFailure(reason types.PoolFailureReason, host types.Host) {}

func (m *mirror) OnReady(sender types.StreamSender, host types.Host) {
	m.sender = sender
	m.host = host

	m.sendDataOnce()
}

func (m *mirror) sendDataOnce() {
	endStream := m.data == nil && m.trailers == nil

	m.sender.AppendHeaders(m.ctx, m.coverHeader(), endStream)

	if endStream {
		return
	}

	endStream = m.trailers == nil
	m.sender.AppendData(m.ctx, m.converData(), endStream)

	if endStream {
		return
	}

	m.sender.AppendTrailers(m.ctx, m.convertTrailer())
}

func (m *mirror) coverHeader() types.HeaderMap {

	dp, up := m.convertProtocol()

	if dp != up {
		convHeader, err := protocol.ConvertHeader(m.ctx, dp, up, m.headers)
		if err == nil {
			return convHeader
		}
		log.Proxy.Warnf(m.ctx, "[proxy] [upstream] [mirror] convert header from %s to %s failed, %s", dp, up, err.Error())
	}
	return m.headers
}

func (m *mirror) converData() types.IoBuffer {
	dp, up := m.convertProtocol()

	if dp != up {
		convData, err := protocol.ConvertData(m.ctx, dp, up, m.data)
		if err == nil {
			return convData
		}
		log.Proxy.Warnf(m.ctx, "[proxy] [upstream] [mirror] convert data from %s to %s failed, %s", dp, up, err.Error())
	}
	return m.data
}

func (m *mirror) convertTrailer() types.HeaderMap {
	dp, up := m.convertProtocol()

	if dp != up {
		convTrailers, err := protocol.ConvertTrailer(m.ctx, dp, up, m.trailers)
		if err == nil {
			return convTrailers
		}
		log.Proxy.Warnf(m.ctx, "[proxy] [upstream] [mirror] convert trailers from %s to %s failed, %s", dp, up, err.Error())
	}
	return m.trailers
}
