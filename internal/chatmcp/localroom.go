package chatmcp

import (
	"context"

	"github.com/jiejhong/GooseCab/internal/agent/localtransport"
	"github.com/jiejhong/GooseCab/internal/protocol"
)

type LocalRoomReader struct {
	client *localtransport.PullClient
}

func ConnectLocalRoom(ctx context.Context, endpoint, room, agent, token string) (*LocalRoomReader, error) {
	client, err := localtransport.DialPull(ctx, endpoint, room, agent, token)
	if err != nil {
		return nil, err
	}
	return &LocalRoomReader{client: client}, nil
}

func ConnectGatewayRoom(ctx context.Context, endpoint, room, agent, token string) (*LocalRoomReader, error) {
	client, err := localtransport.DialGatewayRoom(ctx, endpoint, room, agent, token)
	if err != nil {
		return nil, err
	}
	return &LocalRoomReader{client: client}, nil
}

func (r *LocalRoomReader) Close() error { return r.client.Close() }

type deliveredRoomRead struct {
	value   any
	confirm func(context.Context) error
}

func (r *LocalRoomReader) read(ctx context.Context, args ReadArgs) (any, error) {
	reply, err := r.client.ReadLocalRoomPage(ctx, protocol.LocalRoomReadQuery{
		AfterSeq: args.AfterSeq, AfterCursor: args.AfterCursor, Limit: args.Limit,
	})
	if err != nil {
		return nil, err
	}
	if reply.Receipt == nil {
		return reply.Page, nil
	}
	return deliveredRoomRead{
		value: struct {
			*protocol.LocalRoomPage
			MentionReceipt string `json:"mention_receipt"`
		}{reply.Page, "pending_confirmation"},
		confirm: func(ctx context.Context) error {
			return r.client.ConfirmLocalRoomPage(ctx, *reply.Receipt)
		},
	}, nil
}

func (r *LocalRoomReader) nextMention(ctx context.Context) (any, error) {
	reply, err := r.client.NextMention(ctx)
	if err != nil {
		return nil, err
	}
	value := struct {
		*protocol.LocalRoomPage
		MentionReceipt string `json:"mention_receipt,omitempty"`
	}{LocalRoomPage: reply.Page}
	if reply.Receipt == nil {
		return value, nil
	}
	value.MentionReceipt = "pending_confirmation"
	return deliveredRoomRead{value: value, confirm: func(ctx context.Context) error {
		return r.client.ConfirmLocalRoomPage(ctx, *reply.Receipt)
	}}, nil
}
