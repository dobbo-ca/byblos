package byblos

import (
	"errors"
	"fmt"
)

// ErrNotImplemented reports that a caller asked for something Byblos does not
// do YET, as opposed to something that failed or something about this
// particular document.
//
// The distinction is the whole point, and it is a distinction a caller has to
// be able to make in code rather than by reading a message. Three things can
// go wrong in a call like Optimize and they want three different responses:
//
//	this document is broken        -> park it, review it
//	this document is not eligible  -> divert it, record why (ErrNotSingleRaster)
//	Byblos cannot do this at all   -> fall back to the old tool, for EVERY document
//
// Only the third is a property of the build rather than of the input, so only
// the third should make a caller change its pipeline instead of its handling of
// one file. Retrying, laddering or quarantining a document because the library
// lacks a feature is wasted work at best; at worst it looks like a corpus of
// bad documents and hides the real cause.
//
// Test with errors.Is. To find out WHICH capability is missing, so a caller can
// keep using Byblos for everything else, use errors.As with *NotImplemented.
var ErrNotImplemented = errors.New("byblos: not implemented")

// NotImplemented names a capability Byblos does not have yet, why not, and
// where the work is tracked.
//
// Capability is a capability string from the same vocabulary provenance and
// UpgradeCandidates use (see buildCapabilities in provenance.go and
// capabilityRules in upgrade.go), NOT free text. That is deliberate: it means a
// caller that catches one of these can hand the string straight to
// UpgradeCandidates later to ask whether a newer build would now handle the
// documents it fell back on, instead of maintaining its own parallel list of
// what this version could not do. TestEveryNotImplementedSiteNamesTheRegister
// keeps the two vocabularies from drifting apart.
//
// THAT SENTENCE NAMED A TEST THAT DID NOT EXIST, from byb-b3 until byb-bjh.
// It said TestEveryNotImplementedNamesAKnownCapability enforced this; byb-b3's
// GREEN stage had deleted that test, because its table lost its only row when
// RecompressJPEG stopped returning a *NotImplemented and it would have passed
// vacuously forever after (the tombstone is in optimize_test.go). A doc comment
// promising a guard that was removed is the same defect this whole type exists
// to fix -- a claim with nothing behind it -- so it is recorded here rather
// than quietly corrected.
//
// Issue is the tracking id, so the error itself says where the answer lives
// rather than requiring a search. It is in the message for the same reason. It
// comes from capabilityIssue (upgrade.go) rather than being written at each
// construction site, so the bead an error reports and the bead the register
// tracks cannot be two different answers.
type NotImplemented struct {
	Capability string // e.g. "linearize"
	Why        string // one sentence, in terms of what is missing, not what to do
	Issue      string // e.g. "byb-k48"
}

func (e *NotImplemented) Error() string {
	return fmt.Sprintf("byblos: %s is not implemented: %s (tracked as %s)",
		e.Capability, e.Why, e.Issue)
}

// Unwrap makes errors.Is(err, ErrNotImplemented) true for every NotImplemented,
// so a caller that only wants "can this build do it at all?" does not have to
// know the concrete type.
func (e *NotImplemented) Unwrap() error { return ErrNotImplemented }
