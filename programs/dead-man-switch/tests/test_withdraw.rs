mod common;

use common::*;
use dead_man_switch::error::DmsError;
use solana_signer::Signer;

const MILLION: u64 = 1_000_000;

#[test]
fn returns_lamports_to_the_owner() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 9 * ONE_SOL),
    )
    .expect("deposit failed");

    let owner_before = lamports(&svm, &owner.pubkey());
    send(
        &mut svm,
        &owner,
        withdraw_sol_ix(&owner.pubkey(), &fixture.vault, 4 * ONE_SOL),
    )
    .expect("withdraw_sol failed");

    assert_eq!(vault_available(&svm, &fixture.vault), 5 * ONE_SOL);
    assert!(lamports(&svm, &owner.pubkey()) > owner_before + 4 * ONE_SOL - 100_000);
}

#[test]
fn cannot_dip_into_the_rent_reserve() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 2 * ONE_SOL),
    )
    .expect("deposit failed");

    // One lamport past what was deposited would start eating the reserve that
    // keeps the account alive.
    assert_error(
        send(
            &mut svm,
            &owner,
            withdraw_sol_ix(&owner.pubkey(), &fixture.vault, 2 * ONE_SOL + 1),
        ),
        DmsError::InsufficientFunds,
    );

    send(
        &mut svm,
        &owner,
        withdraw_sol_ix(&owner.pubkey(), &fixture.vault, 2 * ONE_SOL),
    )
    .expect("withdrawing exactly the deposit must work");
    assert_eq!(vault_available(&svm, &fixture.vault), 0);
}

#[test]
fn rejects_a_withdrawal_from_anyone_but_the_owner() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 5 * ONE_SOL),
    )
    .expect("deposit failed");

    // Not even the named heir, and not even after the deadline — they have to
    // go through `claim`, which enforces their share.
    advance_clock(&mut svm, 31 * DAY);
    let heir = &fixture.heirs[0];
    assert_error(
        send(
            &mut svm,
            heir,
            withdraw_sol_ix(&heir.pubkey(), &fixture.vault, 5 * ONE_SOL),
        ),
        DmsError::Unauthorized,
    );
}

#[test]
fn rejects_a_zero_amount() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    assert_error(
        send(
            &mut svm,
            &owner,
            withdraw_sol_ix(&owner.pubkey(), &fixture.vault, 0),
        ),
        DmsError::InvalidAmount,
    );
}

#[test]
fn a_late_owner_can_still_empty_the_vault_before_any_claim() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 6 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 60 * DAY);
    send(
        &mut svm,
        &owner,
        withdraw_sol_ix(&owner.pubkey(), &fixture.vault, 6 * ONE_SOL),
    )
    .expect("the owner keeps control until an heir actually moves");

    assert_eq!(vault_available(&svm, &fixture.vault), 0);
}

#[test]
fn returns_tokens_to_the_owner() {
    let (mut svm, owner) = setup();
    let spl = mint_with_balance(&mut svm, &owner, 100 * MILLION);
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], Some(spl.mint));
    send(
        &mut svm,
        &owner,
        deposit_spl_ix(&owner.pubkey(), &fixture.vault, &spl.mint, 30 * MILLION),
    )
    .expect("deposit_spl failed");

    send(
        &mut svm,
        &owner,
        withdraw_spl_ix(&owner.pubkey(), &fixture.vault, &spl.mint, 12 * MILLION),
    )
    .expect("withdraw_spl failed");

    assert_eq!(
        token_balance(&svm, &ata(&fixture.vault, &spl.mint)),
        18 * MILLION
    );
    assert_eq!(
        token_balance(&svm, &spl.owner_token_account),
        82 * MILLION // 100 minted - 30 deposited + 12 returned
    );
}

#[test]
fn cannot_withdraw_more_tokens_than_the_vault_holds() {
    let (mut svm, owner) = setup();
    let spl = mint_with_balance(&mut svm, &owner, 100 * MILLION);
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], Some(spl.mint));
    send(
        &mut svm,
        &owner,
        deposit_spl_ix(&owner.pubkey(), &fixture.vault, &spl.mint, 5 * MILLION),
    )
    .expect("deposit_spl failed");

    assert_error(
        send(
            &mut svm,
            &owner,
            withdraw_spl_ix(&owner.pubkey(), &fixture.vault, &spl.mint, 6 * MILLION),
        ),
        DmsError::InsufficientFunds,
    );
}

#[test]
fn a_token_withdrawal_cannot_target_a_sol_vault() {
    let (mut svm, owner) = setup();
    let spl = mint_with_balance(&mut svm, &owner, 100 * MILLION);
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    // Anyone can create the vault's token account, so the guard has to hold
    // even when every account in the instruction resolves.
    create_token_account(&mut svm, &owner, &spl.mint, &fixture.vault);

    assert_error(
        send(
            &mut svm,
            &owner,
            withdraw_spl_ix(&owner.pubkey(), &fixture.vault, &spl.mint, MILLION),
        ),
        DmsError::NotSplVault,
    );
}
