package client

import (
	"encoding/binary"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

// Instruction discriminators, taken from the generated IDL rather than
// recomputed from a hash here. A rename in the program then shows up as a
// failing transaction against a stale constant instead of a silently different
// instruction.
var (
	ixInitializeVault = [8]byte{48, 191, 163, 44, 71, 129, 63, 164}
	ixDepositSOL      = [8]byte{108, 81, 78, 117, 125, 155, 56, 200}
	ixCheckIn         = [8]byte{209, 253, 4, 217, 250, 241, 207, 50}
	ixWithdrawSOL     = [8]byte{145, 131, 74, 136, 65, 137, 42, 38}
	ixClaimSOL        = [8]byte{139, 113, 179, 189, 190, 30, 132, 195}
	ixCloseVault      = [8]byte{141, 103, 17, 126, 72, 75, 29, 29}
)

// Heir is one entry of the table passed to `initialize_vault`.
type Heir struct {
	Address  solana.PublicKey
	ShareBPS uint16
}

// Instructions builds the program's instructions and nothing else.
//
// It holds no keys and signs nothing, which is what lets two very different
// callers share it: the CLI, which signs locally with a keypair file, and the
// blink server, which hands an unsigned transaction to a stranger's wallet.
// Both need byte-identical instructions; only the last step differs.
type Instructions struct {
	programID solana.PublicKey
}

func NewInstructions(programID solana.PublicKey) Instructions {
	return Instructions{programID: programID}
}

func (b Instructions) ProgramID() solana.PublicKey { return b.programID }

// VaultAddress derives the PDA for one of an owner's vaults.
func (b Instructions) VaultAddress(owner solana.PublicKey, vaultID uint64) (solana.PublicKey, error) {
	address, _, err := dms.FindVaultAddress(b.programID, owner, vaultID)
	return address, err
}

// InitializeVault opens a native SOL vault.
func (b Instructions) InitializeVault(
	owner solana.PublicKey,
	vault solana.PublicKey,
	vaultID uint64,
	timeout time.Duration,
	heirs []Heir,
) solana.Instruction {
	data := args(ixInitializeVault)
	data = binary.LittleEndian.AppendUint64(data, vaultID)
	data = binary.LittleEndian.AppendUint64(data, uint64(timeout/time.Second))
	// Borsh encodes a Vec as a u32 length followed by the items.
	data = binary.LittleEndian.AppendUint32(data, uint32(len(heirs)))
	for _, heir := range heirs {
		data = append(data, heir.Address.Bytes()...)
		data = binary.LittleEndian.AppendUint16(data, heir.ShareBPS)
	}

	return solana.NewInstruction(b.programID, solana.AccountMetaSlice{
		{PublicKey: owner, IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
		// Anchor's wire convention for an omitted optional account is the
		// program's own id in that slot — this is what makes it a SOL vault.
		{PublicKey: b.programID},
		{PublicKey: solana.SystemProgramID},
	}, data)
}

// DepositSOL funds a native vault.
func (b Instructions) DepositSOL(owner, vault solana.PublicKey, lamports uint64) solana.Instruction {
	return solana.NewInstruction(b.programID, solana.AccountMetaSlice{
		{PublicKey: owner, IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
		{PublicKey: solana.SystemProgramID},
	}, binary.LittleEndian.AppendUint64(args(ixDepositSOL), lamports))
}

// WithdrawSOL takes lamports back out while the switch is still armed.
func (b Instructions) WithdrawSOL(owner, vault solana.PublicKey, lamports uint64) solana.Instruction {
	return solana.NewInstruction(b.programID, solana.AccountMetaSlice{
		{PublicKey: owner, IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
	}, binary.LittleEndian.AppendUint64(args(ixWithdrawSOL), lamports))
}

// CheckIn resets the timer.
func (b Instructions) CheckIn(owner, vault solana.PublicKey) solana.Instruction {
	return solana.NewInstruction(b.programID, solana.AccountMetaSlice{
		{PublicKey: owner, IsSigner: true},
		{PublicKey: vault, IsWritable: true},
	}, args(ixCheckIn))
}

// ClaimSOL takes an heir's share of an expired vault.
func (b Instructions) ClaimSOL(beneficiary, vault solana.PublicKey) solana.Instruction {
	return solana.NewInstruction(b.programID, solana.AccountMetaSlice{
		{PublicKey: beneficiary, IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
	}, args(ixClaimSOL))
}

// CloseVault closes an empty native vault and returns its rent.
func (b Instructions) CloseVault(owner, vault solana.PublicKey) solana.Instruction {
	return solana.NewInstruction(b.programID, solana.AccountMetaSlice{
		{PublicKey: owner, IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
		// Both optional accounts omitted: a SOL vault has no token account.
		{PublicKey: b.programID},
		{PublicKey: b.programID},
	}, args(ixCloseVault))
}

// args starts an instruction payload with its discriminator.
func args(discriminator [8]byte) []byte {
	out := make([]byte, 0, 64)
	return append(out, discriminator[:]...)
}

// UnsignedTransaction packs one instruction into a transaction that `payer`
// has not signed yet.
//
// The signature slots are filled with zeroes rather than left out. A wallet
// deserialising this expects the message header's signer count and that many
// signature slots; omitting them produces bytes that parse as a different,
// malformed transaction.
func UnsignedTransaction(
	instruction solana.Instruction,
	payer solana.PublicKey,
	blockhash solana.Hash,
) (*solana.Transaction, error) {
	tx, err := solana.NewTransaction(
		[]solana.Instruction{instruction},
		blockhash,
		solana.TransactionPayer(payer),
	)
	if err != nil {
		return nil, err
	}

	tx.Signatures = make([]solana.Signature, tx.Message.Header.NumRequiredSignatures)

	return tx, nil
}
