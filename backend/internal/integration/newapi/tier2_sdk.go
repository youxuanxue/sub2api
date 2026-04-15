package newapi

import (
	"errors"
	"strings"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/stripe/stripe-go/v81"
	waffo "github.com/waffo-com/waffo-go"
	"github.com/waffo-com/waffo-go/config"
)

type EPayConfig struct {
	PartnerID string
	Key       string
	BaseURL   string
}

// NewEPayClient builds an EPay SDK client for TokenKey integrations.
func NewEPayClient(cfg EPayConfig) (*epay.Client, error) {
	if strings.TrimSpace(cfg.PartnerID) == "" || strings.TrimSpace(cfg.Key) == "" || strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("epay requires partner_id, key and base_url")
	}
	return epay.NewClient(&epay.Config{
		PartnerID: cfg.PartnerID,
		Key:       cfg.Key,
	}, cfg.BaseURL)
}

type WaffoConfig struct {
	Sandbox      bool
	APIKey       string
	PrivateKey   string
	PublicCert   string
	MerchantID   string
	DefaultMoney string
	DefaultTitle string
}

// NewWaffoClient builds a Waffo SDK client using the same builder style as New API.
func NewWaffoClient(cfg WaffoConfig) (*waffo.Waffo, error) {
	env := config.Production
	if cfg.Sandbox {
		env = config.Sandbox
	}
	builder := config.NewConfigBuilder().
		APIKey(strings.TrimSpace(cfg.APIKey)).
		PrivateKey(strings.TrimSpace(cfg.PrivateKey)).
		WaffoPublicKey(strings.TrimSpace(cfg.PublicCert)).
		Environment(env)
	if id := strings.TrimSpace(cfg.MerchantID); id != "" {
		builder = builder.MerchantID(id)
	}
	c, err := builder.Build()
	if err != nil {
		return nil, err
	}
	return waffo.New(c), nil
}

type StripeCheckoutInput struct {
	SecretKey     string
	ReferenceID   string
	CustomerID    string
	CustomerEmail string
	AmountCents   int64
	Currency      string
	ProductName   string
	SuccessURL    string
	CancelURL     string
}

// BuildStripeCheckoutParams returns SDK params for creating a Stripe checkout session.
func BuildStripeCheckoutParams(in StripeCheckoutInput) *stripe.CheckoutSessionParams {
	_ = strings.TrimSpace(in.SecretKey) // caller may apply to stripe.Key at runtime.
	currency := strings.ToLower(strings.TrimSpace(in.Currency))
	if currency == "" {
		currency = "usd"
	}
	name := strings.TrimSpace(in.ProductName)
	if name == "" {
		name = "TokenKey Topup"
	}
	p := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(strings.TrimSpace(in.ReferenceID)),
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL:        stripe.String(strings.TrimSpace(in.SuccessURL)),
		CancelURL:         stripe.String(strings.TrimSpace(in.CancelURL)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Quantity: stripe.Int64(1),
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency: stripe.String(currency),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name: stripe.String(name),
					},
					UnitAmount: stripe.Int64(in.AmountCents),
				},
			},
		},
	}
	if id := strings.TrimSpace(in.CustomerID); id != "" {
		p.Customer = stripe.String(id)
	} else if email := strings.TrimSpace(in.CustomerEmail); email != "" {
		p.CustomerEmail = stripe.String(email)
	}
	return p
}
