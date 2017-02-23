package rpc

import (
	"context"
	"io"
	"io/ioutil"
	"math"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sourcegraph/jsonrpc2"
	websocketrpc "github.com/sourcegraph/jsonrpc2/websocket"
)

const (
	methodNext   = "next"
	methodNotify = "notify"
	methodUpdate = "update"
	methodLog    = "log"
	methodSave   = "save"
)

type (
	saveReq struct {
		ID   string `json:"id"`
		Mime string `json:"mime"`
		Data []byte `json:"data"`
	}

	updateReq struct {
		ID    string `json:"id"`
		State State  `json:"state"`
	}

	logReq struct {
		ID   string `json:"id"`
		Line *Line  `json:"line"`
	}
)

const (
	defaultRetryClount = math.MaxInt32
	defaultBackoff     = 10 * time.Second
)

// Client represents an rpc client.
type Client struct {
	sync.Mutex

	conn     *jsonrpc2.Conn
	done     bool
	retry    int
	backoff  time.Duration
	endpoint string
}

// NewClient returns a new Client.
func NewClient(endpoint string, opts ...Option) (*Client, error) {
	cli := &Client{
		endpoint: endpoint,
		retry:    defaultRetryClount,
		backoff:  defaultBackoff,
	}
	for _, opt := range opts {
		opt(cli)
	}
	err := cli.openRetry()
	return cli, err
}

// Next returns the next pipeline in the queue.
func (t *Client) Next(c context.Context) (*Pipeline, error) {
	res := new(Pipeline)
	err := t.call(methodNext, nil, res)
	return res, err
}

// Notify returns true if the pipeline should be cancelled.
func (t *Client) Notify(c context.Context, id string) (bool, error) {
	out := false
	err := t.call(methodNotify, id, &out)
	return out, err
}

// Update updates the pipeline state.
func (t *Client) Update(c context.Context, id string, state State) error {
	params := updateReq{id, state}
	return t.call(methodUpdate, &params, nil)
}

// Log writes the pipeline log entry.
func (t *Client) Log(c context.Context, id string, line *Line) error {
	params := logReq{id, line}
	return t.call(methodLog, &params, nil)
}

// Save saves the pipeline artifact.
func (t *Client) Save(c context.Context, id, mime string, file io.Reader) error {
	data, err := ioutil.ReadAll(file)
	if err != nil {
		return err
	}
	params := saveReq{id, mime, data}
	return t.call(methodSave, params, nil)
}

// Close closes the client connection.
func (t *Client) Close() error {
	t.Lock()
	t.done = true
	t.Unlock()
	return t.conn.Close()
}

// call makes the remote prodedure call. If the call fails due to connectivity
// issues the connection is re-establish and call re-attempted.
func (t *Client) call(name string, req, res interface{}) error {
	if err := t.conn.Call(context.Background(), name, req, res); err == nil {
		return nil
	} else if err != jsonrpc2.ErrClosed && err != io.ErrUnexpectedEOF {
		return err
	}
	if err := t.openRetry(); err != nil {
		return err
	}
	return t.conn.Call(context.Background(), name, req, res)
}

// openRetry opens the connection and will retry on failure until
// the connection is successfully open, or the maximum retry count
// is exceeded.
func (t *Client) openRetry() error {
	for i := 0; i < t.retry; i++ {
		err := t.open()
		if err == nil {
			break
		}
		if err == io.EOF {
			return err
		}
		<-time.After(t.backoff)
	}
	return nil
}

// open creates a websocket connection to a peer and establishes a json
// rpc communication stream.
func (t *Client) open() error {
	t.Lock()
	defer t.Unlock()
	if t.done {
		return io.EOF
	}
	conn, _, err := websocket.DefaultDialer.Dial(t.endpoint, nil)
	if err != nil {
		return err
	}
	stream := websocketrpc.NewObjectStream(conn)
	t.conn = jsonrpc2.NewConn(context.Background(), stream, nil)
	return nil
}
