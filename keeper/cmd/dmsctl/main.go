// Command dmsctl drives a Dead Man's Switch vault from the terminal.
//
// It exists to exercise a deployed program end to end — open a vault, fund it,
// check in, let it expire, claim — without a browser or a TypeScript
// toolchain. The keeper never imports this package: signing belongs to a
// person, not to a service.
//
//	dmsctl open    --id 1 --timeout 30d --heir <pubkey>:7000 --heir <pubkey>:3000
//	dmsctl deposit --id 1 --sol 0.5
//	dmsctl check-in --id 1
//	dmsctl status  --id 1
//	dmsctl claim   --vault <pubkey>
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/client"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

const usage = `dmsctl — drive a Dead Man's Switch vault

commands:
  open      open a vault and name its heirs
  deposit   move SOL into a vault
  withdraw  take SOL back out, while the switch is still armed
  check-in  reset the timer
  claim     take your share of an expired vault
  close     close an empty vault and reclaim its rent
  status    show one vault, or every vault the program owns

common flags:
  -rpc      RPC endpoint            (default https://api.devnet.solana.com)
  -program  program id              (default the devnet deployment)
  -wallet   keypair file            (default ~/.config/solana/id.json)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type globals struct {
	rpc     string
	program string
	wallet  string
}

func (g *globals) bind(fs *flag.FlagSet) {
	home, _ := os.UserHomeDir()
	fs.StringVar(&g.rpc, "rpc", envOr("DMS_RPC_ENDPOINT", "https://api.devnet.solana.com"), "RPC endpoint")
	fs.StringVar(&g.program, "program", envOr("DMS_PROGRAM_ID", "9tfSr7zg9bGBfpSqqdwCACiSfAqsbdE4rNnwezFe5Ldm"), "program id")
	fs.StringVar(&g.wallet, "wallet", envOr("DMS_WALLET", home+"/.config/solana/id.json"), "keypair file")
}

func (g *globals) open() (*client.Client, solana.PublicKey, error) {
	programID, err := solana.PublicKeyFromBase58(g.program)
	if err != nil {
		return nil, solana.PublicKey{}, fmt.Errorf("program id: %w", err)
	}
	payer, err := solana.PrivateKeyFromSolanaKeygenFile(g.wallet)
	if err != nil {
		return nil, solana.PublicKey{}, fmt.Errorf("wallet %s: %w", g.wallet, err)
	}

	return client.New(g.rpc, programID, payer), programID, nil
}

func run(argv []string) error {
	if len(argv) == 0 {
		fmt.Print(usage)
		return errors.New("no command given")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	command, rest := argv[0], argv[1:]
	switch command {
	case "open":
		return cmdOpen(ctx, rest)
	case "deposit":
		return cmdMoveSOL(ctx, rest, "deposit")
	case "withdraw":
		return cmdMoveSOL(ctx, rest, "withdraw")
	case "check-in":
		return cmdCheckIn(ctx, rest)
	case "claim":
		return cmdClaim(ctx, rest)
	case "close":
		return cmdClose(ctx, rest)
	case "status":
		return cmdStatus(ctx, rest)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

// heirList collects repeated -heir <pubkey>:<bps> flags.
type heirList []client.Heir

func (h *heirList) String() string { return fmt.Sprintf("%d heirs", len(*h)) }

func (h *heirList) Set(raw string) error {
	address, share, ok := strings.Cut(raw, ":")
	if !ok {
		return fmt.Errorf("want <pubkey>:<basis points>, got %q", raw)
	}
	key, err := solana.PublicKeyFromBase58(address)
	if err != nil {
		return fmt.Errorf("heir %q: %w", address, err)
	}
	bps, err := strconv.ParseUint(share, 10, 16)
	if err != nil {
		return fmt.Errorf("share %q: %w", share, err)
	}

	*h = append(*h, client.Heir{Address: key, ShareBPS: uint16(bps)})

	return nil
}

func cmdOpen(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	var g globals
	g.bind(fs)
	id := fs.Uint64("id", 1, "vault id — lets one wallet hold several vaults")
	timeout := fs.String("timeout", "720h", "how long silence may last, e.g. 30d, 720h, 5m")
	var heirs heirList
	fs.Var(&heirs, "heir", "<pubkey>:<basis points>, repeatable; shares must total 10000")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if len(heirs) == 0 {
		return errors.New("a vault needs at least one -heir")
	}

	window, err := parseDuration(*timeout)
	if err != nil {
		return err
	}

	c, _, err := g.open()
	if err != nil {
		return err
	}

	vault, signature, err := c.InitializeVault(ctx, *id, window, heirs)
	if err != nil {
		return err
	}

	fmt.Printf("vault    %s\n", vault)
	fmt.Printf("owner    %s\n", c.Payer())
	fmt.Printf("timeout  %s\n", window)
	for _, heir := range heirs {
		fmt.Printf("heir     %s  %.2f%%\n", heir.Address, float64(heir.ShareBPS)/100)
	}
	printSignature(signature, g.rpc)

	return nil
}

func cmdMoveSOL(ctx context.Context, argv []string, direction string) error {
	fs := flag.NewFlagSet(direction, flag.ExitOnError)
	var g globals
	g.bind(fs)
	id := fs.Uint64("id", 1, "vault id")
	amount := fs.Float64("sol", 0, "amount in SOL")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *amount <= 0 {
		return errors.New("-sol must be greater than zero")
	}

	c, _, err := g.open()
	if err != nil {
		return err
	}
	vault, err := c.VaultAddress(*id)
	if err != nil {
		return err
	}

	lamports := uint64(*amount * float64(solana.LAMPORTS_PER_SOL))

	var signature solana.Signature
	if direction == "deposit" {
		signature, err = c.DepositSOL(ctx, vault, lamports)
	} else {
		signature, err = c.WithdrawSOL(ctx, vault, lamports)
	}
	if err != nil {
		return err
	}

	fmt.Printf("%sed %g SOL %s vault %s\n", direction, *amount, preposition(direction), vault)
	printSignature(signature, g.rpc)

	return nil
}

func preposition(direction string) string {
	if direction == "deposit" {
		return "into"
	}
	return "from"
}

func cmdCheckIn(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("check-in", flag.ExitOnError)
	var g globals
	g.bind(fs)
	id := fs.Uint64("id", 1, "vault id")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	c, _, err := g.open()
	if err != nil {
		return err
	}
	vault, err := c.VaultAddress(*id)
	if err != nil {
		return err
	}

	signature, err := c.CheckIn(ctx, vault)
	if err != nil {
		return err
	}

	fmt.Printf("checked in on vault %s\n", vault)
	printSignature(signature, g.rpc)

	return nil
}

func cmdClaim(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("claim", flag.ExitOnError)
	var g globals
	g.bind(fs)
	// An heir knows the vault address, not the owner's vault id, so this one
	// takes the address directly.
	address := fs.String("vault", "", "vault address")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	vault, err := solana.PublicKeyFromBase58(*address)
	if err != nil {
		return fmt.Errorf("-vault: %w", err)
	}

	c, _, err := g.open()
	if err != nil {
		return err
	}

	signature, err := c.ClaimSOL(ctx, vault)
	if err != nil {
		return err
	}

	fmt.Printf("claimed from vault %s as %s\n", vault, c.Payer())
	printSignature(signature, g.rpc)

	return nil
}

func cmdClose(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("close", flag.ExitOnError)
	var g globals
	g.bind(fs)
	id := fs.Uint64("id", 1, "vault id")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	c, _, err := g.open()
	if err != nil {
		return err
	}
	vault, err := c.VaultAddress(*id)
	if err != nil {
		return err
	}

	signature, err := c.CloseVault(ctx, vault)
	if err != nil {
		return err
	}

	fmt.Printf("closed vault %s; rent returned to %s\n", vault, c.Payer())
	printSignature(signature, g.rpc)

	return nil
}

func cmdStatus(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	var g globals
	g.bind(fs)
	id := fs.Int64("id", -1, "vault id; omit to list every vault the program owns")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	programID, err := solana.PublicKeyFromBase58(g.program)
	if err != nil {
		return fmt.Errorf("program id: %w", err)
	}
	reader := dms.NewClient(g.rpc, programID)

	var vaults []*dms.Vault
	if *id < 0 {
		if vaults, err = reader.Vaults(ctx); err != nil {
			return err
		}
	} else {
		c, _, err := g.open()
		if err != nil {
			return err
		}
		address, err := c.VaultAddress(uint64(*id))
		if err != nil {
			return err
		}
		vault, err := reader.Vault(ctx, address)
		if err != nil {
			return err
		}
		vaults = []*dms.Vault{vault}
	}

	if len(vaults) == 0 {
		fmt.Println("no vaults")
		return nil
	}

	now := time.Now().UTC()
	for i, vault := range vaults {
		if i > 0 {
			fmt.Println()
		}
		printVault(vault, now)
	}

	return nil
}

func printVault(v *dms.Vault, now time.Time) {
	asset := "SOL"
	if !v.IsSOL() {
		asset = "SPL " + v.Mint.String()
	}

	fmt.Printf("vault     %s\n", v.Address)
	fmt.Printf("owner     %s\n", v.Owner)
	fmt.Printf("id        %d\n", v.VaultID)
	fmt.Printf("asset     %s\n", asset)
	fmt.Printf("status    %s\n", v.Status(now, 14*24*time.Hour))
	fmt.Printf("balance   %.9f SOL\n", float64(v.Lamports)/float64(solana.LAMPORTS_PER_SOL))
	fmt.Printf("checked   %s\n", v.LastCheckIn.Format(time.RFC3339))
	fmt.Printf("deadline  %s (%s)\n", v.Deadline().Format(time.RFC3339), leftOrOverdue(v.TimeLeft(now)))
	if v.IsClaimed {
		fmt.Printf("pool      %.9f SOL snapshotted, %d of %d heirs paid\n",
			float64(v.ClaimPool)/float64(solana.LAMPORTS_PER_SOL), v.Claimed(), len(v.Beneficiaries))
	}
	for _, heir := range v.Beneficiaries {
		mark := " "
		if heir.Claimed {
			mark = "✓"
		}
		fmt.Printf("heir    %s %s  %.2f%%\n", mark, heir.Address, heir.SharePercent())
	}
}

func leftOrOverdue(d time.Duration) string {
	if d < 0 {
		return "overdue by " + (-d).Round(time.Second).String()
	}
	return d.Round(time.Second).String() + " left"
}

func printSignature(signature solana.Signature, endpoint string) {
	fmt.Printf("tx       %s\n", signature)
	if strings.Contains(endpoint, "devnet") {
		fmt.Printf("explorer https://explorer.solana.com/tx/%s?cluster=devnet\n", signature)
	}
}

// parseDuration accepts Go durations plus a `d` suffix for days, because
// nobody wants to write 720h for a month.
func parseDuration(raw string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return 0, fmt.Errorf("timeout %q: %w", raw, err)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("timeout %q: %w", raw, err)
	}

	return d, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
