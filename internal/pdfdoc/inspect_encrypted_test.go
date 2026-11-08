package pdfdoc_test

// Owner decision Enc (kleio in-process pipeline design, 10.1): an owner-password-only
// document opens and Inspect says it was encrypted; one that needs a user
// password is refused with ErrPassword before any page is read.

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dobbo-ca/byblos"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func TestInspectDocumentEncryption(t *testing.T) {
	di, err := byblos.InspectDocument(context.Background(), bytes.NewReader(ownerPasswordOnlyFixture(t)), byblos.Limits{})
	if err != nil {
		t.Fatalf("owner-password-only: %v", err)
	}
	if !di.Encrypted || len(di.Pages) != 1 {
		t.Errorf("owner-password-only: %+v; want Encrypted with 1 page", di)
	}

	di, err = byblos.InspectDocument(context.Background(), bytes.NewReader(plaintextFixture(t)), byblos.Limits{})
	if err != nil || di.Encrypted {
		t.Errorf("plaintext: %+v, %v; want not Encrypted", di, err)
	}

	var out bytes.Buffer
	conf := model.NewAESConfiguration("user", "owner", 128)
	if err := api.Encrypt(context.Background(), bytes.NewReader(plaintextFixture(t)), &out, conf); err != nil {
		t.Fatal(err)
	}
	if _, err := byblos.InspectDocument(context.Background(), bytes.NewReader(out.Bytes()), byblos.Limits{}); !errors.Is(err, byblos.ErrPassword) {
		t.Errorf("user-password document: err = %v; want ErrPassword", err)
	}
}
