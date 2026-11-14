package peerclient

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NetworkOptions picks a peer and a client identity from the files that
// luthersystems/fabric-network-builder generates.
type NetworkOptions struct {
	// Dir holds crypto-config/.
	Dir string
	// Org is the org name, such as "org1". Empty means the first org in
	// crypto-config/peerOrganizations.
	Org string
	// Peer is the peer's host name in the org, such as "peer0".
	Peer string
	// User is the user whose TLS and signing identity the client uses, such
	// as "User1". A member of the org is a channel reader.
	User string
	// Address is the peer's host:port. The builder publishes peer0.org1 on
	// 127.0.0.1:7051.
	Address string
	Channel string
}

// NetworkConfig returns the Config for a fabric-network-builder network. It
// finds the org's directory, crypto-config/peerOrganizations/ORG.DOMAIN, so
// it needs no option the network was generated with.
func NetworkConfig(o NetworkOptions) (Config, error) {
	orgsDir := filepath.Join(o.Dir, "crypto-config", "peerOrganizations")
	entries, err := os.ReadDir(orgsDir)
	if err != nil {
		return Config{}, fmt.Errorf("read the network's crypto material (run fabric-network-builder generate first): %w", err)
	}
	var orgDomain string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.Contains(name, ".") {
			continue
		}
		if o.Org == "" || strings.HasPrefix(name, o.Org+".") {
			orgDomain = name
			break
		}
	}
	if orgDomain == "" {
		if o.Org == "" {
			return Config{}, fmt.Errorf("no org in %s", orgsDir)
		}
		return Config{}, fmt.Errorf("no org %s in %s", o.Org, orgsDir)
	}
	org := orgDomain[:strings.Index(orgDomain, ".")]
	userDir := filepath.Join(orgsDir, orgDomain, "users", o.User+"@"+orgDomain)
	signCert, err := FirstFile(filepath.Join(userDir, "msp", "signcerts"))
	if err != nil {
		return Config{}, fmt.Errorf("user %s signing certificate: %w", o.User, err)
	}
	signKey, err := FirstFile(filepath.Join(userDir, "msp", "keystore"))
	if err != nil {
		return Config{}, fmt.Errorf("user %s signing key: %w", o.User, err)
	}
	return Config{
		Address:       o.Address,
		ServerName:    o.Peer + "." + orgDomain,
		TLSRootCAFile: filepath.Join(userDir, "tls", "ca.crt"),
		TLSCertFile:   filepath.Join(userDir, "tls", "client.crt"),
		TLSKeyFile:    filepath.Join(userDir, "tls", "client.key"),
		MSPID:         mspID(org),
		SignCertFile:  signCert,
		SignKeyFile:   signKey,
		Channel:       o.Channel,
	}, nil
}

// mspID is the MSP ID the builder gives an org: "org1" is "Org1MSP".
func mspID(org string) string {
	if org == "" {
		return ""
	}
	return strings.ToUpper(org[:1]) + org[1:] + "MSP"
}
