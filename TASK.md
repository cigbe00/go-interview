# Senior Backend Engineer — Practical Exercise

## Objective

We want to evaluate how you approach an unfamiliar Go backend with minimal supervision: reading existing code, identifying root causes, fixing API/data bugs, integrating external providers cleanly, writing tests, and communicating technical decisions.

This repository is intentionally small. It contains mock data and a few deliberate defects. Please treat it as production code you have just inherited.

## Your tasks

### 1. Debug the existing API

Several API behaviours are incorrect. Investigate and fix the root causes rather than patching response payloads.

Known user-visible symptoms include:

- Creating a review may return success, but the business review count does not reliably reflect the new review.
- A business's average rating can be incorrect.
- Review pagination can return an unexpected number of records.
- At least one business lookup edge case returns incorrect behaviour.

You may find additional issues. Fix anything you consider materially incorrect and explain it in your PR.

### 2. Implement Google sign-in verification

Complete the Google ID-token verification path behind the existing `auth.TokenVerifier` interface.

Requirements:

- Do not hard-code credentials.
- Read configuration from environment variables.
- Validate that the token belongs to the configured Google client/audience.
- Validate the important token claims needed to trust the identity.
- Return a normalized user identity to the service layer.
- Map provider/network/invalid-token failures into sensible API errors.
- Add tests without requiring real Google credentials or outbound network access.

If you choose not to make a real Google network call in the exercise, provide a production-ready implementation structure and tests that demonstrate how it behaves using a local/mock HTTP server.

### 3. Implement Paystack subscription initialization + webhook handling

Complete the Paystack client and subscription flow behind the existing payment interfaces.

Requirements:

- Initialize a subscription/transaction through a clean provider client.
- Do not hard-code the Paystack secret key.
- Use context-aware HTTP requests and reasonable timeouts.
- Handle non-2xx responses and malformed provider responses safely.
- Implement webhook signature verification.
- Make webhook/event processing idempotent so the same event cannot apply a subscription change twice.
- Keep provider transport concerns separate from business logic.
- Add tests using `httptest.Server` or equivalent; no real Paystack credentials should be necessary.

### 4. Improve quality where appropriate

We will look for pragmatic senior-level improvements such as:

- Clear error handling and status codes
- Correct context propagation
- Focused unit/integration tests
- Safe concurrency where relevant
- Maintainable interfaces and package boundaries
- Avoiding unnecessary abstractions
- Useful logs or comments where they add value

Do **not** rewrite the whole application or replace Echo. We want to see how you improve an inherited codebase.

## Submission

1. Fork/checkout the repository.
2. Create a branch for your work.
3. Commit your changes with meaningful commit messages.
4. Open a Pull Request.
5. In the PR description, include:
   - Bugs/root causes you identified
   - What you changed and why
   - Trade-offs or assumptions
   - What you would improve next with more time
   - How you would monitor these flows in production

## Evaluation

We care more about correctness, reasoning, tests, and maintainability than raw volume of code. A working but fragile patch will score lower than a clean fix that shows you understand the failure mode.
