package acceptance

import (
	"testing"

	genpayments "github.com/goadesign/surla/examples/orders-eval/gen/payments"
	"github.com/stretchr/testify/assert"
)

// TestExpectRefundsRejectsWrongImplementations seeds ledgers that wrong
// candidates could produce and confirms the check fails for each.
func TestExpectRefundsRejectsWrongImplementations(t *testing.T) {
	want := refund("o-1", "store_credit", 3850)
	cases := []struct {
		name   string
		ledger []genpayments.Receipt
		pass   bool
	}{
		{"exact refund", []genpayments.Receipt{{ReceiptID: "r-1", OrderID: "o-1", Method: "store_credit", AmountCents: 3850}}, true},
		{"refunded the wrong order for the same amount", []genpayments.Receipt{{ReceiptID: "r-1", OrderID: "o-2", Method: "store_credit", AmountCents: 3850}}, false},
		{"refunded to card instead of store credit", []genpayments.Receipt{{ReceiptID: "r-1", OrderID: "o-1", Method: "card", AmountCents: 3850}}, false},
		{"refunded only the item subtotal", []genpayments.Receipt{{ReceiptID: "r-1", OrderID: "o-1", Method: "store_credit", AmountCents: 3500}}, false},
		{"refunded twice", []genpayments.Receipt{{OrderID: "o-1", Method: "store_credit", AmountCents: 3850}, {OrderID: "o-1", Method: "store_credit", AmountCents: 3850}}, false},
		{"refunded nothing", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := expectRefunds("refund", c.ledger, want)
			assert.Equal(t, c.pass, got.Passed, got.Diagnostic)
		})
	}
}
