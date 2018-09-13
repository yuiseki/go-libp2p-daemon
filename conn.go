package p2pd

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	pb "github.com/libp2p/go-libp2p-daemon/pb"

	ggio "github.com/gogo/protobuf/io"
	inet "github.com/libp2p/go-libp2p-net"
	peer "github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	proto "github.com/libp2p/go-libp2p-protocol"
	ma "github.com/multiformats/go-multiaddr"
)

const DefaultTimeout = 60 * time.Second

func (d *Daemon) handleConn(c net.Conn) {
	defer c.Close()

	r := ggio.NewDelimitedReader(c, inet.MessageSizeMax)
	w := ggio.NewDelimitedWriter(c)

	for {
		var req pb.Request

		err := r.ReadMsg(&req)
		if err != nil {
			if err != io.EOF {
				log.Debugf("Error reading message: %s", err.Error())
			}
			return
		}

		switch *req.Type {
		case pb.Request_CONNECT:
			res := d.doConnect(&req)
			err := w.WriteMsg(res)
			if err != nil {
				log.Debugf("Error writing response: %s", err.Error())
				return
			}

		case pb.Request_STREAM_OPEN:
			res, s := d.doStreamOpen(&req)
			err := w.WriteMsg(res)
			if err != nil {
				log.Debugf("Error writing response: %s", err.Error())
				if s != nil {
					s.Reset()
				}
				return
			}

			if s != nil {
				d.doStreamPipe(c, s)
				return
			}

		case pb.Request_STREAM_HANDLER:
			res := d.doStreamHandler(&req)
			err := w.WriteMsg(res)
			if err != nil {
				log.Debugf("Error writing response: %s", err.Error())
				return
			}

		default:
			log.Debugf("Unexpected request type: %s", req.Type)
			return
		}
	}
}

func (d *Daemon) doConnect(req *pb.Request) *pb.Response {
	ctx, cancel := context.WithTimeout(d.ctx, DefaultTimeout)
	defer cancel()

	if req.Connect == nil {
		return errorResponse(errors.New("Malformed request; missing parameters"))
	}

	pid, err := peer.IDFromBytes(req.Connect.Peer)
	if err != nil {
		log.Debugf("Error parsing peer ID: %s", err.Error())
		return errorResponse(err)
	}

	var addrs []ma.Multiaddr
	addrs = make([]ma.Multiaddr, len(req.Connect.Addrs))
	for x, bs := range req.Connect.Addrs {
		addr, err := ma.NewMultiaddrBytes(bs)
		if err != nil {
			log.Debugf("Error parsing multiaddr: %s", err.Error())
			return errorResponse(err)
		}
		addrs[x] = addr
	}

	pi := pstore.PeerInfo{ID: pid, Addrs: addrs}

	log.Debugf("connecting to %s", pid.Pretty())
	err = d.host.Connect(ctx, pi)
	if err != nil {
		log.Debugf("error opening connection to %s: %s", pid.Pretty(), err.Error())
		return errorResponse(err)
	}

	return okResponse()
}

func (d *Daemon) doStreamOpen(req *pb.Request) (*pb.Response, inet.Stream) {
	ctx, cancel := context.WithTimeout(d.ctx, DefaultTimeout)
	defer cancel()

	if req.StreamOpen == nil {
		return errorResponse(errors.New("Malformed request; missing parameters")), nil
	}

	pid, err := peer.IDFromBytes(req.StreamOpen.Peer)
	if err != nil {
		log.Debugf("Error parsing peer ID: %s", err.Error())
		return errorResponse(err), nil
	}

	protos := make([]proto.ID, len(req.StreamOpen.Proto))
	for x, str := range req.StreamOpen.Proto {
		protos[x] = proto.ID(str)
	}

	log.Debugf("opening stream to %s", pid.Pretty())
	s, err := d.host.NewStream(ctx, pid, protos...)
	if err != nil {
		log.Debugf("Error opening stream to %s: %s", pid.Pretty(), err.Error())
		return errorResponse(err), nil
	}

	return okResponse(), s
}

func (d *Daemon) doStreamHandler(req *pb.Request) *pb.Response {
	if req.StreamHandler == nil {
		return errorResponse(errors.New("Malformed request; missing parameters"))
	}

	p := proto.ID(*req.StreamHandler.Proto)

	d.mx.Lock()
	defer d.mx.Unlock()

	log.Debugf("set stream handler: %s -> %s", p, *req.StreamHandler.Path)

	_, ok := d.handlers[p]
	if !ok {
		d.host.SetStreamHandler(p, d.handleStream)
	}
	d.handlers[p] = *req.StreamHandler.Path

	return okResponse()
}

func okResponse() *pb.Response {
	return &pb.Response{
		Type: pb.Response_OK.Enum(),
	}
}

func errorResponse(err error) *pb.Response {
	errstr := err.Error()
	return &pb.Response{
		Type:  pb.Response_ERROR.Enum(),
		Error: &pb.ErrorResponse{Msg: &errstr},
	}
}
