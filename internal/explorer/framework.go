package explorer

import (
	"encoding/json"
	"strings"
	"time"
)

// FrameworkSpace is a key space that substrate reserves for the framework
// (luthersystems/substrate docs/agents/reserved-keys.md). A key matches the
// longest Prefix, or the key equal to an Exact space.
type FrameworkSpace struct {
	Prefix string `json:"space"`
	Owner  string `json:"owner"`
	Exact  bool   `json:"exact,omitempty"`
}

// frameworkSpaces is substrate's registry of reserved key spaces. A key under
// "luther:" that no named space covers is in the "luther:" catch-all.
var frameworkSpaces = []FrameworkSpace{
	{Prefix: "luther:flow:", Owner: "libflow"},
	{Prefix: "luther:phylum:", Owner: "router"},
	{Prefix: "luther:app_control", Owner: "appctrl", Exact: true},
	{Prefix: "luther:app_control:", Owner: "appctrl"},
	{Prefix: "luther:private:", Owner: "private"},
	{Prefix: "luther:pvtdata:health:", Owner: "private"},
	{Prefix: "luther:batch:", Owner: "batch"},
	{Prefix: "luther:batchreq:", Owner: "batch"},
	{Prefix: "luther:cr:", Owner: "connector"},
	{Prefix: "luther:ev:", Owner: "connector"},
	{Prefix: "luther:auth:", Owner: "auth"},
	{Prefix: "luther:healthcheck:", Owner: "healthcheck"},
	{Prefix: "utl:migration:", Owner: "migration"},
	{Prefix: "utl:migration-ctx:", Owner: "migration"},
	{Prefix: "utl:migration-frontier:", Owner: "migration"},
	{Prefix: "luther:", Owner: "framework"},
}

// FrameworkSpaces returns the known framework key spaces.
func FrameworkSpaces() []FrameworkSpace {
	return append([]FrameworkSpace(nil), frameworkSpaces...)
}

// frameworkSpaceOf returns the space a key is in, or "" for a phylum key.
// The longest match wins; an exact key wins over a prefix of its length.
func frameworkSpaceOf(key string) string {
	best := ""
	bestExact := false
	for _, s := range frameworkSpaces {
		var match bool
		if s.Exact {
			match = key == s.Prefix
		} else {
			match = strings.HasPrefix(key, s.Prefix)
		}
		if !match {
			continue
		}
		if len(s.Prefix) > len(best) || (len(s.Prefix) == len(best) && s.Exact && !bestExact) {
			best, bestExact = s.Prefix, s.Exact
		}
	}
	return best
}

// Phylum storage keys (luthersystems/substrate
// internal/substrate/phylumroute/phylumroute.go).
const (
	phylumDescPrefix = "luther:phylum:desc:"
	phylumSrcPrefix  = "luther:phylum:src:"
)

// PhylumVersion is a phylum version and the transactions that changed it.
type PhylumVersion struct {
	Namespace     string        `json:"namespace"`
	Version       string        `json:"version"`
	Status        string        `json:"status"`
	Fingerprint   string        `json:"fingerprint,omitempty"`
	InitTimestamp string        `json:"init_timestamp,omitempty"`
	Events        []PhylumEvent `json:"events"`
	SourceSize    int           `json:"source_size,omitempty"`
	Deleted       bool          `json:"deleted,omitempty"`
}

// PhylumEvent is one change to a phylum version's description.
type PhylumEvent struct {
	TxRef
	// Action is install (the first description), enable, disable, update
	// (another change) or delete.
	Action string `json:"action"`
	Status string `json:"status,omitempty"`
}

// TxRef points at a transaction.
type TxRef struct {
	Time        *time.Time `json:"time,omitempty"`
	TxID        string     `json:"tx_id"`
	Method      string     `json:"method,omitempty"`
	BlockNumber uint64     `json:"block_number"`
	TxIndex     int        `json:"tx_index"`
}

// phylumDescription is the value router:store-phylum writes.
type phylumDescription struct {
	PhylumID      json.RawMessage `json:"phylum_id"`
	Fingerprint   json.RawMessage `json:"fingerprint"`
	Status        string          `json:"status"`
	InitTimestamp string          `json:"init_timestamp"`
}

// rawString returns a JSON string's text, or the raw JSON of another value.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// phylumAction names the change from one description status to the next.
func phylumAction(prev *PhylumVersion, status string) string {
	switch {
	case prev == nil || prev.Deleted:
		return "install"
	case prev.Status != status && status == "DISABLED":
		return "disable"
	case prev.Status != status && status == "IN_SERVICE":
		return "enable"
	default:
		return "update"
	}
}
