package payments

import "context"

type FakeProvider struct {
	InitResp     InitializeResponse
	InitErr      error
	SignatureErr error
	Event        WebhookEvent
	ParseErr     error
}

func (f FakeProvider) Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error) {
	return f.InitResp, f.InitErr
}
func (f FakeProvider) VerifyWebhookSignature(body []byte, sig string) error { return f.SignatureErr }
func (f FakeProvider) ParseWebhook(body []byte) (WebhookEvent, error)       { return f.Event, f.ParseErr }
