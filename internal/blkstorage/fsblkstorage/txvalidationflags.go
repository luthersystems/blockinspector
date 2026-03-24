/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Copied from github.com/hyperledger/fabric@v2.1.1 core/ledger/util/txvalidationflags.go

package fsblkstorage

import (
	"github.com/hyperledger/fabric-protos-go/peer"
)

// TxValidationFlags is array of transaction validation codes.
type TxValidationFlags []uint8

// Flag returns validation code at specified transaction
func (obj TxValidationFlags) Flag(txIndex int) peer.TxValidationCode {
	return peer.TxValidationCode(obj[txIndex])
}
