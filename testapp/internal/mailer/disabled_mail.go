package mailer

// TwoFactorDisabledMail is a value object created inline, NOT a DI provider.
// autodi should NOT include this because no constructor parameter references it.
type TwoFactorDisabledMail struct {
	A, B, C int
}

// NewTwoFactorDisabledMail creates a mail value object.
// Called inline like: NewTwoFactorDisabledMail(1, 2, 3)
func NewTwoFactorDisabledMail(a, b, c int) *TwoFactorDisabledMail {
	return &TwoFactorDisabledMail{A: a, B: b, C: c}
}
