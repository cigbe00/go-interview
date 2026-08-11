package auth

import "context"

type FakeVerifier struct {
	Identity Identity
	Err      error
}

func (f FakeVerifier) Verify(ctx context.Context, idToken string) (Identity, error) {
	if f.Err != nil {
		return Identity{}, f.Err
	}
	return f.Identity, nil
}
