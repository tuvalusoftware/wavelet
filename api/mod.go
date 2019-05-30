// Copyright (c) 2019 Perlin
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of
// this software and associated documentation files (the "Software"), to deal in
// the Software without restriction, including without limitation the rights to
// use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
// the Software, and to permit persons to whom the Software is furnished to do so,
// subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
// FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
// COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
// IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
// CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package api

import (
	"encoding/hex"
	"fmt"
	"github.com/buaazp/fasthttprouter"
	"github.com/perlin-network/noise/skademlia"
	"github.com/perlin-network/wavelet"
	"github.com/perlin-network/wavelet/log"
	"github.com/pkg/errors"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/pprofhandler"
	"github.com/valyala/fastjson"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Gateway struct {
	client *skademlia.Client
	ledger *wavelet.Ledger

	network *skademlia.Protocol
	keys    *skademlia.Keypair

	router   *fasthttprouter.Router
	server   *fasthttp.Server
	registry *sessionRegistry
	sinks    map[string]*sink

	parserPool *fastjson.ParserPool
	arenaPool  *fastjson.ArenaPool
}

func New() *Gateway {
	return &Gateway{
		registry:   newSessionRegistry(),
		sinks:      make(map[string]*sink),
		parserPool: new(fastjson.ParserPool),
		arenaPool:  new(fastjson.ArenaPool),
	}
}

func (g *Gateway) setup(enableTimeout bool) {
	// Setup websocket logging sinks.

	sinkNetwork := g.registerWebsocketSink("ws://network/")
	sinkBroadcaster := g.registerWebsocketSink("ws://broadcaster/")
	sinkConsensus := g.registerWebsocketSink("ws://consensus/")
	sinkStake := g.registerWebsocketSink("ws://stake/")
	sinkAccounts := g.registerWebsocketSink("ws://accounts/?id=account_id")
	sinkContracts := g.registerWebsocketSink("ws://contract/?id=contract_id")
	sinkTransactions := g.registerWebsocketSink("ws://tx/?id=tx_id&sender=sender_id&creator=creator_id")
	sinkMetrics := g.registerWebsocketSink("ws://metrics/")

	log.Set("ws", g)

	// Setup HTTP router.

	r := fasthttprouter.New()

	// If the route does not exist for a method type (e.g. OPTIONS), fasthttprouter will consider it to not exist.
	// So, we need to override notFound handler for OPTIONS method type to handle CORS.
	r.HandleOPTIONS = false
	r.NotFound = g.notFound()

	var base = []middleware{
		recoverer,
		cors(),
	}

	if enableTimeout {
		base = append(base, timeout(60*time.Second, "Request timeout!"))
	}

	var authenticated = append(base, g.authenticated)
	var contract = append(authenticated, g.contractScope)

	// Websocket endpoints.
	r.GET("/poll/network", g.securePoll(sinkNetwork))
	r.GET("/poll/broadcaster", chain(g.securePoll(sinkBroadcaster), base))
	r.GET("/poll/consensus", chain(g.securePoll(sinkConsensus), base))
	r.GET("/poll/stake", chain(g.securePoll(sinkStake), base))
	r.GET("/poll/accounts", chain(g.securePoll(sinkAccounts), base))
	r.GET("/poll/contract", chain(g.securePoll(sinkContracts), base))
	r.GET("/poll/tx", chain(g.securePoll(sinkTransactions), base))
	r.GET("/poll/metrics", chain(g.securePoll(sinkMetrics), base))

	// Debug endpoint.
	r.GET("/debug/*p", pprofhandler.PprofHandler)

	// Session endpoint.
	r.POST("/session/init", chain(g.initSession, base))

	// Ledger endpoint.
	r.GET("/ledger", chain(g.ledgerStatus, authenticated))

	// Account endpoints.
	r.GET("/accounts/:id", chain(g.getAccount, authenticated))

	// Contract endpoints.
	r.GET("/contract/:id/page/:index", chain(g.getContractPages, contract))
	r.GET("/contract/:id/page", chain(g.getContractPages, contract))
	r.GET("/contract/:id", chain(g.getContractCode, contract))

	// Transaction endpoints.
	r.POST("/tx/send", chain(g.sendTransaction, authenticated))
	r.GET("/tx/:id", chain(g.getTransaction, authenticated))
	r.GET("/tx", chain(g.listTransactions, authenticated))

	g.router = r
}

func (g *Gateway) StartHTTP(port int, c *skademlia.Client, l *wavelet.Ledger, k *skademlia.Keypair) {
	g.client = c
	g.ledger = l

	g.keys = k

	g.setup(false)

	logger := log.Node()
	logger.Info().Int("port", port).Msg("Started HTTP API server.")

	g.server = &fasthttp.Server{
		Handler: g.router.Handler,
	}

	if err := g.server.ListenAndServe(":" + strconv.Itoa(port)); err != nil {
		logger.Fatal().Err(err).Msg("Failed to start HTTP server.")
	}
}

func (g *Gateway) Shutdown() {
	if g.server == nil {
		return
	}
	_ = g.server.Shutdown()
}

func (g *Gateway) initSession(ctx *fasthttp.RequestCtx) {
	req := new(sessionInitRequest)

	parser := g.parserPool.Get()
	err := req.bind(parser, ctx.PostBody())
	g.parserPool.Put(parser)

	if err != nil {
		g.renderError(ctx, ErrBadRequest(err))
		return
	}

	session, err := g.registry.newSession()
	if err != nil {
		g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "failed to create session")))
		return
	}

	g.render(ctx, &sessionInitResponse{Token: session.id})
}

func (g *Gateway) sendTransaction(ctx *fasthttp.RequestCtx) {
	req := new(sendTransactionRequest)

	parser := g.parserPool.Get()
	err := req.bind(parser, ctx.PostBody())
	g.parserPool.Put(parser)

	if err != nil {
		g.renderError(ctx, ErrBadRequest(err))
		return
	}

	tx := wavelet.AttachSenderToTransaction(
		g.keys,
		wavelet.Transaction{Tag: req.Tag, Payload: req.payload, Creator: req.creator, CreatorSignature: req.signature},
		g.ledger.Graph().FindEligibleParents()...,
	)

	err = g.ledger.AddTransaction(tx)

	if err != nil && errors.Cause(err) != wavelet.ErrMissingParents {
		g.renderError(ctx, ErrInternal(errors.Wrap(err, "error adding your transaction to graph")))
		return
	}

	g.render(ctx, &sendTransactionResponse{ledger: g.ledger, tx: &tx})
}

func (g *Gateway) ledgerStatus(ctx *fasthttp.RequestCtx) {
	g.render(ctx, &ledgerStatusResponse{client: g.client, ledger: g.ledger, publicKey: g.keys.PublicKey()})
}

func (g *Gateway) listTransactions(ctx *fasthttp.RequestCtx) {
	var sender wavelet.AccountID
	var creator wavelet.AccountID
	var offset, limit uint64
	var err error

	queryArgs := ctx.QueryArgs()
	if raw := string(queryArgs.Peek("sender")); len(raw) > 0 {
		slice, err := hex.DecodeString(raw)

		if err != nil {
			g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "sender ID must be presented as valid hex")))
			return
		}

		if len(slice) != wavelet.SizeAccountID {
			g.renderError(ctx, ErrBadRequest(errors.Errorf("sender ID must be %d bytes long", wavelet.SizeAccountID)))
			return
		}

		copy(sender[:], slice)
	}

	if raw := string(queryArgs.Peek("creator")); len(raw) > 0 {
		slice, err := hex.DecodeString(raw)

		if err != nil {
			g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "creator ID must be presented as valid hex")))
			return
		}

		if len(slice) != wavelet.SizeAccountID {
			g.renderError(ctx, ErrBadRequest(errors.Errorf("creator ID must be %d bytes long", wavelet.SizeAccountID)))
			return
		}

		copy(creator[:], slice)
	}

	if raw := string(queryArgs.Peek("offset")); len(raw) > 0 {
		offset, err = strconv.ParseUint(raw, 10, 64)

		if err != nil {
			g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "could not parse offset")))
			return
		}
	}

	if raw := string(queryArgs.Peek("limit")); len(raw) > 0 {
		limit, err = strconv.ParseUint(raw, 10, 64)

		if err != nil {
			g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "could not parse limit")))
			return
		}
	}

	rootDepth := g.ledger.Graph().RootDepth()

	var transactions transactionList

	for _, tx := range g.ledger.Graph().ListTransactions(offset, limit, sender, creator) {
		status := "received"

		if tx.Depth <= rootDepth {
			status = "applied"
		}

		transactions = append(transactions, &transaction{tx: tx, status: status})
	}

	g.render(ctx, transactions)
}

func (g *Gateway) getTransaction(ctx *fasthttp.RequestCtx) {
	param, ok := ctx.UserValue("id").(string)
	if !ok {
		g.renderError(ctx, ErrBadRequest(errors.New("id must be a string")))
		return
	}

	slice, err := hex.DecodeString(param)
	if err != nil {
		g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "transaction ID must be presented as valid hex")))
		return
	}

	if len(slice) != wavelet.SizeTransactionID {
		g.renderError(ctx, ErrBadRequest(errors.Errorf("transaction ID must be %d bytes long", wavelet.SizeTransactionID)))
		return
	}

	var id wavelet.TransactionID
	copy(id[:], slice)

	tx := g.ledger.Graph().FindTransaction(id)

	if tx == nil {
		g.renderError(ctx, ErrBadRequest(errors.Errorf("could not find transaction with ID %x", id)))
		return
	}

	rootDepth := g.ledger.Graph().RootDepth()

	res := &transaction{tx: tx}

	if tx.Depth <= rootDepth {
		res.status = "applied"
	} else {
		res.status = "received"
	}

	g.render(ctx, res)
}

func (g *Gateway) getAccount(ctx *fasthttp.RequestCtx) {
	param, ok := ctx.UserValue("id").(string)
	if !ok {
		g.renderError(ctx, ErrBadRequest(errors.New("id must be a string")))
		return
	}

	slice, err := hex.DecodeString(param)
	if err != nil {
		g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "account ID must be presented as valid hex")))
		return
	}

	if len(slice) != wavelet.SizeAccountID {
		g.renderError(ctx, ErrBadRequest(errors.Errorf("account ID must be %d bytes long", wavelet.SizeAccountID)))
		return
	}

	var id wavelet.AccountID
	copy(id[:], slice)

	g.render(ctx, &account{ledger: g.ledger, id: id})
}

func (g *Gateway) contractScope(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return fasthttp.RequestHandler(func(ctx *fasthttp.RequestCtx) {
		param, ok := ctx.UserValue("id").(string)
		if !ok {
			g.renderError(ctx, ErrBadRequest(errors.New("could not cast id into string")))
			return
		}

		slice, err := hex.DecodeString(param)
		if err != nil {
			g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "contract ID must be presented as valid hex")))
			return
		}

		if len(slice) != wavelet.SizeTransactionID {
			g.renderError(ctx, ErrBadRequest(errors.Errorf("contract ID must be %d bytes long", wavelet.SizeTransactionID)))
			return
		}

		var contractID wavelet.TransactionID
		copy(contractID[:], slice)

		ctx.SetUserValue("contract_id", contractID)

		next(ctx)
	})
}

func (g *Gateway) getContractCode(ctx *fasthttp.RequestCtx) {
	id, ok := ctx.UserValue("contract_id").(wavelet.TransactionID)
	if !ok {
		g.renderError(ctx, ErrBadRequest(errors.New("id must be a TransactionID")))
		return
	}

	code, available := wavelet.ReadAccountContractCode(g.ledger.Snapshot(), id)

	if len(code) == 0 || !available {
		g.renderError(ctx, ErrBadRequest(errors.Errorf("could not find contract with ID %x", id)))
		return
	}

	ctx.Response.Header.Set("Content-Disposition", "attachment; filename="+hex.EncodeToString(id[:])+".wasm")
	ctx.Response.Header.Set("Content-Type", "application/wasm")
	ctx.Response.Header.Set("Content-Length", strconv.Itoa(hex.EncodedLen(len(code))))

	_, _ = io.Copy(ctx, strings.NewReader(hex.EncodeToString(code)))
}

func (g *Gateway) getContractPages(ctx *fasthttp.RequestCtx) {
	id, ok := ctx.UserValue("contract_id").(wavelet.TransactionID)
	if !ok {
		g.renderError(ctx, ErrBadRequest(errors.New("id must be a TransactionID")))
		return
	}

	var idx uint64
	var err error

	rawIdx, ok := ctx.UserValue("index").(string)
	if !ok {
		g.renderError(ctx, ErrBadRequest(errors.New("could not cast index into string")))
		return
	}

	if len(rawIdx) != 0 {
		idx, err = strconv.ParseUint(rawIdx, 10, 64)

		if err != nil {
			g.renderError(ctx, ErrBadRequest(errors.New("could not parse page index")))
			return
		}
	}

	snapshot := g.ledger.Snapshot()

	numPages, available := wavelet.ReadAccountContractNumPages(snapshot, id)

	if !available {
		g.renderError(ctx, ErrBadRequest(errors.Errorf("could not find any pages for contract with ID %x", id)))
		return
	}

	if idx >= numPages {
		g.renderError(ctx, ErrBadRequest(errors.Errorf("contract with ID %x only has %d pages, but you requested page %d", id, numPages, idx)))
		return
	}

	page, available := wavelet.ReadAccountContractPage(snapshot, id, idx)

	if len(page) == 0 || !available {
		g.renderError(ctx, ErrBadRequest(errors.Errorf("page %d is either empty, or does not exist", idx)))
		return
	}

	_, _ = ctx.Write(page)
}

func (g *Gateway) notFound() func(ctx *fasthttp.RequestCtx) {
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

	notFoundHandler := func(ctx *fasthttp.RequestCtx) {
		ctx.Error(fasthttp.StatusMessage(fasthttp.StatusNotFound),
			fasthttp.StatusNotFound)
	}

	// This cors is only for OPTIONS, so we can pass any handler since it will not be triggered.
	cors := cors()(notFoundHandler)

	lookupCtx := &fasthttp.RequestCtx{}

	return func(ctx *fasthttp.RequestCtx) {
		if string(ctx.Method()) != "OPTIONS" {
			notFoundHandler(ctx)
			return
		}

		path := string(ctx.Path())

		// Only proceed to cors if the route really exist.
		// We try to look the route for other method types.
		for _, m := range methods {
			h, _ := g.router.Lookup(m, path, lookupCtx)
			if h != nil {
				cors(ctx)
				return
			}
		}

		notFoundHandler(ctx)
	}
}

func (g *Gateway) authenticated(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return fasthttp.RequestHandler(func(ctx *fasthttp.RequestCtx) {
		token := string(ctx.Request.Header.Peek(HeaderSessionToken))
		if len(token) == 0 {
			g.renderError(ctx, ErrBadRequest(errors.Errorf("session token not specified via HTTP header %q", HeaderSessionToken)))
			return
		}

		session, exists := g.registry.getSession(token)
		if !exists {
			g.renderError(ctx, ErrBadRequest(errors.Errorf("could not find session %s", token)))
			return
		}

		ctx.SetUserValue(KeySession, session)
		next(ctx)
	})
}

func (g *Gateway) securePoll(sink *sink) func(ctx *fasthttp.RequestCtx) {
	return func(ctx *fasthttp.RequestCtx) {
		token := string(ctx.QueryArgs().Peek(KeyToken))

		if len(token) == 0 {
			g.renderError(ctx, ErrBadRequest(errors.New("specify a session token through url query params")))
			return
		}

		if _, exists := g.registry.getSession(token); !exists {
			g.renderError(ctx, ErrBadRequest(errors.Errorf("could not find session %s", token)))
			return
		}

		if err := sink.serve(ctx); err != nil {
			g.renderError(ctx, ErrBadRequest(errors.Wrap(err, "failed to init websocket session")))
		}
	}
}

func (g *Gateway) registerWebsocketSink(rawURL string) *sink {
	u, err := url.Parse(rawURL)

	if err != nil {
		panic(err)
	}

	// Map JSON log keys to HTTP query parameters.
	filters := make(map[string]string)
	values := u.Query()

	for key := range values {
		filters[key] = values.Get(key)
	}

	sink := &sink{
		filters:   filters,
		broadcast: make(chan broadcastItem),
		join:      make(chan *client),
		leave:     make(chan *client),
		clients:   make(map[*client]struct{}),
	}
	go sink.run()

	g.sinks[u.Hostname()] = sink

	return sink
}

func (g *Gateway) Write(buf []byte) (n int, err error) {
	var p fastjson.Parser

	v, err := p.ParseBytes(buf)

	if err != nil {
		return n, errors.Errorf("cannot parse: %q", err)
	}

	mod := v.GetStringBytes(log.KeyModule)
	if mod == nil {
		return n, errors.Errorf("all logs must have the field %q", log.KeyModule)
	}

	sink, exists := g.sinks[string(mod)]
	if !exists {
		return len(buf), nil
	}

	cpy := make([]byte, len(buf))
	copy(cpy, buf)

	sink.broadcast <- broadcastItem{value: v, buf: cpy}

	return len(buf), nil
}

func (g *Gateway) render(ctx *fasthttp.RequestCtx, m marshalableJSON) {
	arena := g.arenaPool.Get()
	b, err := m.marshalJSON(arena)
	g.arenaPool.Put(arena)

	if err != nil {
		ctx.Error(fmt.Sprintf(`{ "error": "render error: %s" }`, err.Error()), http.StatusInternalServerError)
		return
	}

	ctx.SetContentType("application/json")
	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(b)
}

func (g *Gateway) renderError(ctx *fasthttp.RequestCtx, e *errResponse) {
	arena := g.arenaPool.Get()
	b, err := e.marshalJSON(arena)
	g.arenaPool.Put(arena)

	if err != nil {
		ctx.Error(fmt.Sprintf(`{ "error": "render error: %s" |`, err.Error()), http.StatusInternalServerError)
		return
	}

	ctx.SetContentType("application/json")
	ctx.Response.SetStatusCode(e.HTTPStatusCode)
	ctx.Response.SetBody(b)
}