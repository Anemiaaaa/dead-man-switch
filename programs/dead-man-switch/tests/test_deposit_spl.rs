mod common;

use common::*;
use dead_man_switch::error::DmsError;
use solana_signer::Signer;

const MILLION: u64 = 1_000_000;

#[test]
fn opens_a_vault_pinned_to_its_mint() {
    let (mut svm, owner) = setup();
    let fixture = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let (vault_key, _heir) = open_spl_vault(&mut svm, &owner, 1, 30 * DAY, &fixture.mint);

    let vault = read_vault(&svm, &vault_key);
    assert_eq!(vault.mint, Some(fixture.mint));
    assert!(!vault.is_sol());
}

#[test]
fn moves_tokens_into_an_account_the_vault_controls() {
    let (mut svm, owner) = setup();
    let fixture = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let (vault_key, _heir) = open_spl_vault(&mut svm, &owner, 1, 30 * DAY, &fixture.mint);

    let instruction = deposit_spl_ix(&owner.pubkey(), &vault_key, &fixture.mint, 4 * MILLION);
    send(&mut svm, &owner, instruction).expect("deposit_spl failed");

    let vault_ata = ata(&vault_key, &fixture.mint);
    assert_eq!(token_balance(&svm, &vault_ata), 4 * MILLION);
    assert_eq!(
        token_balance(&svm, &fixture.owner_token_account),
        6 * MILLION
    );

    // The lock: the vault PDA is the token account's authority, not the owner.
    let state = litesvm_token::get_spl_account::<litesvm_token::spl_token::state::Account>(
        &svm, &vault_ata,
    )
    .unwrap();
    assert_eq!(state.owner, vault_key);
    assert_eq!(state.mint, fixture.mint);
}

#[test]
fn deposits_accumulate() {
    let (mut svm, owner) = setup();
    let fixture = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let (vault_key, _heir) = open_spl_vault(&mut svm, &owner, 1, 30 * DAY, &fixture.mint);

    for _ in 0..3 {
        let instruction = deposit_spl_ix(&owner.pubkey(), &vault_key, &fixture.mint, MILLION);
        send(&mut svm, &owner, instruction).expect("deposit_spl failed");
    }

    assert_eq!(
        token_balance(&svm, &ata(&vault_key, &fixture.mint)),
        3 * MILLION
    );
}

#[test]
fn rejects_a_zero_amount() {
    let (mut svm, owner) = setup();
    let fixture = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let (vault_key, _heir) = open_spl_vault(&mut svm, &owner, 1, 30 * DAY, &fixture.mint);

    let instruction = deposit_spl_ix(&owner.pubkey(), &vault_key, &fixture.mint, 0);

    assert_error(send(&mut svm, &owner, instruction), DmsError::InvalidAmount);
}

#[test]
fn rejects_a_mint_the_vault_was_not_opened_for() {
    let (mut svm, owner) = setup();
    let expected = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let other = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let (vault_key, _heir) = open_spl_vault(&mut svm, &owner, 1, 30 * DAY, &expected.mint);

    let instruction = deposit_spl_ix(&owner.pubkey(), &vault_key, &other.mint, MILLION);

    assert_error(send(&mut svm, &owner, instruction), DmsError::MintMismatch);
}

#[test]
fn rejects_a_token_deposit_into_a_sol_vault() {
    let (mut svm, owner) = setup();
    let fixture = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let (vault_key, _heir) = open_sol_vault(&mut svm, &owner, 1, 30 * DAY);

    let instruction = deposit_spl_ix(&owner.pubkey(), &vault_key, &fixture.mint, MILLION);

    assert_error(send(&mut svm, &owner, instruction), DmsError::NotSplVault);
}

#[test]
fn rejects_a_lamport_deposit_into_a_token_vault() {
    let (mut svm, owner) = setup();
    let fixture = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let (vault_key, _heir) = open_spl_vault(&mut svm, &owner, 1, 30 * DAY, &fixture.mint);

    let instruction = deposit_sol_ix(&owner.pubkey(), &vault_key, ONE_SOL);

    assert_error(send(&mut svm, &owner, instruction), DmsError::NotSolVault);
}

#[test]
fn rejects_a_deposit_from_anyone_but_the_owner() {
    let (mut svm, owner) = setup();
    let fixture = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let (vault_key, _heir) = open_spl_vault(&mut svm, &owner, 1, 30 * DAY, &fixture.mint);
    let stranger = funded_wallet(&mut svm);

    // The stranger holds real tokens of the right mint — the only thing
    // stopping them is that the vault is not theirs.
    fund_token_account(
        &mut svm,
        &owner,
        &fixture.mint,
        &stranger.pubkey(),
        5 * MILLION,
    );

    let instruction = deposit_spl_ix(&stranger.pubkey(), &vault_key, &fixture.mint, MILLION);

    assert_error(
        send(&mut svm, &stranger, instruction),
        DmsError::Unauthorized,
    );
}
