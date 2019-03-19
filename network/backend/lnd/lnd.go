package lnd

import (
	"context"
	"fmt"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/edouardparis/lntop/config"
	"github.com/edouardparis/lntop/logging"
	"github.com/edouardparis/lntop/network/backend/pool"
	"github.com/edouardparis/lntop/network/models"
	"github.com/edouardparis/lntop/network/options"
)

const (
	lndDefaultInvoiceExpiry = 3600
)

type Client struct {
	lnrpc.LightningClient
	conn *pool.Conn
}

func (c *Client) Close() error {
	return c.conn.Close()
}

type Backend struct {
	cfg    *config.Network
	logger logging.Logger
	pool   *pool.Pool
}

func (l Backend) NodeName() string {
	return l.cfg.ID
}

func (l Backend) SubscribeInvoice(ctx context.Context, channelInvoice chan *models.Invoice) error {
	clt, err := l.Client(ctx)
	if err != nil {
		return err
	}

	cltInvoices, err := clt.SubscribeInvoices(ctx, &lnrpc.InvoiceSubscription{})
	if err != nil {
		return err
	}

	for {
		invoice, err := cltInvoices.Recv()
		if err != nil {
			return err
		}

		channelInvoice <- lookupInvoiceProtoToInvoice(invoice)
	}
}

func (l Backend) Client(ctx context.Context) (*Client, error) {
	conn, err := l.pool.Get(ctx)
	if err != nil {
		return nil, err
	}

	l.logger.Debug("Client connection retrieved", logging.String("target", conn.Target()))

	return &Client{
		LightningClient: lnrpc.NewLightningClient(conn.ClientConn),
		conn:            conn,
	}, nil
}

func (l Backend) NewClientConn() (*grpc.ClientConn, error) {
	return newClientConn(l.cfg)
}

func (l Backend) GetWalletBalance(ctx context.Context) (*models.WalletBalance, error) {
	l.logger.Debug("Retrieve wallet balance...")

	clt, err := l.Client(ctx)
	if err != nil {
		return nil, err
	}
	defer clt.Close()

	req := &lnrpc.WalletBalanceRequest{}
	resp, err := clt.WalletBalance(ctx, req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	balance := protoToWalletBalance(resp)

	l.logger.Debug("Wallet balance retrieved", logging.Object("wallet", balance))

	return balance, nil
}

func (l Backend) GetChannelBalance(ctx context.Context) (*models.ChannelBalance, error) {
	l.logger.Debug("Retrieve channel balance...")

	clt, err := l.Client(ctx)
	if err != nil {
		return nil, err
	}
	defer clt.Close()

	req := &lnrpc.ChannelBalanceRequest{}
	resp, err := clt.ChannelBalance(ctx, req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	balance := protoToChannelBalance(resp)

	l.logger.Debug("Channel balance retrieved", logging.Object("balance", balance))

	return balance, nil
}

func (l Backend) ListChannels(ctx context.Context, opt ...options.Channel) ([]*models.Channel, error) {
	l.logger.Debug("List channels")

	clt, err := l.Client(ctx)
	if err != nil {
		return nil, err
	}
	defer clt.Close()

	opts := options.NewChannelOptions(opt...)
	req := &lnrpc.ListChannelsRequest{
		ActiveOnly:   opts.Active,
		InactiveOnly: opts.Inactive,
		PublicOnly:   opts.Public,
		PrivateOnly:  opts.Private,
	}

	resp, err := clt.ListChannels(ctx, req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	channels := listChannelsProtoToChannels(resp)

	fields := make([]logging.Field, len(channels))

	for i := range channels {
		fields[i] = logging.Object(fmt.Sprintf("channel_%d", i), channels[i])
	}

	l.logger.Debug("Channels retrieved", fields...)

	return channels, nil
}

func (l Backend) CreateInvoice(ctx context.Context, amount int64, desc string) (*models.Invoice, error) {
	l.logger.Debug("Create invoice...",
		logging.Int64("amount", amount),
		logging.String("desc", desc))

	clt, err := l.Client(ctx)
	if err != nil {
		return nil, err
	}
	defer clt.Close()

	req := &lnrpc.Invoice{
		Value:        amount,
		Memo:         desc,
		CreationDate: time.Now().Unix(),
		Expiry:       lndDefaultInvoiceExpiry,
	}

	resp, err := clt.AddInvoice(ctx, req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	invoice := addInvoiceProtoToInvoice(req, resp)

	l.logger.Debug("Invoice retrieved", logging.Object("invoice", invoice))

	return invoice, nil
}

func (l Backend) GetInvoice(ctx context.Context, RHash string) (*models.Invoice, error) {
	l.logger.Debug("Retrieve invoice...", logging.String("r_hash", RHash))

	clt, err := l.Client(ctx)
	if err != nil {
		return nil, err
	}
	defer clt.Close()

	req := &lnrpc.PaymentHash{
		RHashStr: RHash,
	}

	resp, err := clt.LookupInvoice(ctx, req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	invoice := lookupInvoiceProtoToInvoice(resp)

	l.logger.Debug("Invoice retrieved", logging.Object("invoice", invoice))

	return invoice, nil
}

func (l Backend) SendPayment(ctx context.Context, payreq *models.PayReq) (*models.Payment, error) {
	l.logger.Debug("Send payment...",
		logging.String("destination", payreq.Destination),
		logging.Int64("amount", payreq.Amount),
	)

	clt, err := l.Client(ctx)
	if err != nil {
		return nil, err
	}
	defer clt.Close()

	req := &lnrpc.SendRequest{PaymentRequest: payreq.String}

	resp, err := clt.SendPaymentSync(ctx, req)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	payment := sendPaymentProtoToPayment(payreq, resp)

	l.logger.Debug("Payment paid", logging.Object("payment", payment))

	return payment, nil
}

func (l Backend) DecodePayReq(ctx context.Context, payreq string) (*models.PayReq, error) {
	l.logger.Info("decode payreq", logging.String("payreq", payreq))
	clt, err := l.Client(ctx)
	if err != nil {
		return nil, err
	}
	defer clt.Close()

	resp, err := clt.DecodePayReq(ctx, &lnrpc.PayReqString{PayReq: payreq})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return payreqProtoToPayReq(resp, payreq), nil
}

func New(c *config.Network, logger logging.Logger) (*Backend, error) {
	var err error

	backend := &Backend{
		cfg:    c,
		logger: logger.With(logging.String("id", c.ID)),
	}

	backend.pool, err = pool.New(backend.NewClientConn, c.PoolCapacity, time.Duration(c.ConnTimeout))
	if err != nil {
		return nil, err
	}

	return backend, nil
}