package payments

import "context"

type FakeProvider struct {
	InitResp     InitializeResponse
	InitErr      error
	SignatureErr error
	Event        WebhookEvent
	ParseErr     error
}

func (f FakeProvider) Initialize(context.Context, InitializeRequest) (InitializeResponse, error) {
	return f.InitResp, f.InitErr
}
func (f FakeProvider) VerifyWebhookSignature([]byte, string) error { return f.SignatureErr }
func (f FakeProvider) ParseWebhook([]byte) (WebhookEvent, error)   { return f.Event, f.ParseErr }
