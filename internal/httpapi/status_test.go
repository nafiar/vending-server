package httpapi

import (
	"net/http"
	"testing"

	"vending/internal/model"
)

// Every code the rules can raise must have a status. Without this, a new code
// would answer 500 and the screens would show "Terjadi kesalahan di server"
// instead of the message the rule wrote.
func TestStatusForCoversEveryCode(t *testing.T) {
	for _, code := range model.Codes {
		if _, ok := statusByCode[code]; !ok {
			t.Errorf("%s has no HTTP status: add it to statusByCode", code)
		}
	}
	for code := range statusByCode {
		found := false
		for _, known := range model.Codes {
			if known == code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("statusByCode has %s, which is not in model.Codes", code)
		}
	}
}

// An unknown code is a fault in the rules, so it must not pass as a success.
func TestStatusForUnknownCodeIsServerFault(t *testing.T) {
	if got := statusFor("SOMETHING_NEW"); got != http.StatusInternalServerError {
		t.Errorf("statusFor(unknown) = %d, want 500", got)
	}
}
