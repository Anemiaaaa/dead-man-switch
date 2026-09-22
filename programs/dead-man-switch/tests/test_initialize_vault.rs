mod common;

use anchor_lang::{prelude::Pubkey, Space};
use common::*;
use dead_man_switch::{error::DmsError, state::Vault};
use solana_keypair::Keypair;
use solana_signer::Signer;

#[test]
fn opens_a_native_sol_vault() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();
    let bob = Keypair::new().pubkey();

    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        1,
        30 * DAY,
        heirs(&[(alice, 7_000), (bob, 3_000)]),
        None,
    );
    send(&mut svm, &owner, instruction).expect("initialize_vault failed");

    let vault_key = vault_pda(&owner.pubkey(), 1);
    let vault = read_vault(&svm, &vault_key);

    assert_eq!(vault.owner, owner.pubkey());
    assert_eq!(vault.vault_id, 1);
    assert_eq!(vault.mint, None);
    assert_eq!(vault.timeout_seconds, 30 * DAY);
    assert_eq!(vault.beneficiary_count, 2);
    assert!(!vault.is_claimed);
    assert_eq!(vault.claim_pool, 0);

    // The timer starts the moment the vault opens.
    assert_eq!(vault.last_checkin, GENESIS);
    assert_eq!(vault.deadline().unwrap(), GENESIS + 30 * DAY);
    assert!(!vault.is_expired(GENESIS + 30 * DAY - 1).unwrap());
    assert!(vault.is_expired(GENESIS + 30 * DAY).unwrap());

    let active = vault.active();
    assert_eq!(active.len(), 2);
    assert_eq!(active[0].address, alice);
    assert_eq!(active[0].share_bps, 7_000);
    assert!(!active[0].claimed);
    assert_eq!(active[1].address, bob);
    assert_eq!(active[1].share_bps, 3_000);

    // Unused slots stay zeroed and out of `active()`.
    assert_eq!(vault.beneficiaries[2].address, Pubkey::default());
    assert_eq!(vault.beneficiaries[2].share_bps, 0);
}

#[test]
fn rejects_shares_that_do_not_add_up_to_one_hundred_percent() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();
    let bob = Keypair::new().pubkey();

    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        1,
        30 * DAY,
        heirs(&[(alice, 6_000), (bob, 3_000)]),
        None,
    );

    assert_error(send(&mut svm, &owner, instruction), DmsError::InvalidShares);
}

#[test]
fn rejects_a_zero_share() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();
    let bob = Keypair::new().pubkey();

    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        1,
        30 * DAY,
        heirs(&[(alice, 10_000), (bob, 0)]),
        None,
    );

    assert_error(send(&mut svm, &owner, instruction), DmsError::InvalidShares);
}

#[test]
fn rejects_the_same_heir_listed_twice() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();

    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        1,
        30 * DAY,
        heirs(&[(alice, 5_000), (alice, 5_000)]),
        None,
    );

    assert_error(
        send(&mut svm, &owner, instruction),
        DmsError::DuplicateBeneficiary,
    );
}

#[test]
fn rejects_an_empty_heir_table() {
    let (mut svm, owner) = setup();

    let instruction = initialize_vault_ix(&owner.pubkey(), 1, 30 * DAY, vec![], None);

    assert_error(
        send(&mut svm, &owner, instruction),
        DmsError::NoBeneficiaries,
    );
}

#[test]
fn rejects_more_heirs_than_the_vault_can_hold() {
    let (mut svm, owner) = setup();
    let six: Vec<(Pubkey, u16)> = (0..6)
        .map(|i| (Keypair::new().pubkey(), if i == 0 { 5_000 } else { 1_000 }))
        .collect();

    let instruction = initialize_vault_ix(&owner.pubkey(), 1, 30 * DAY, heirs(&six), None);

    assert_error(
        send(&mut svm, &owner, instruction),
        DmsError::TooManyBeneficiaries,
    );
}

#[test]
fn rejects_the_default_pubkey_as_an_heir() {
    let (mut svm, owner) = setup();

    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        1,
        30 * DAY,
        heirs(&[(Pubkey::default(), 10_000)]),
        None,
    );

    assert_error(
        send(&mut svm, &owner, instruction),
        DmsError::InvalidBeneficiary,
    );
}

#[test]
fn rejects_a_timeout_below_the_minimum() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();

    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        1,
        30, // under MIN_TIMEOUT_SECONDS
        heirs(&[(alice, 10_000)]),
        None,
    );

    assert_error(
        send(&mut svm, &owner, instruction),
        DmsError::InvalidTimeout,
    );
}

#[test]
fn rejects_a_timeout_above_the_maximum() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();

    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        1,
        100 * 365 * DAY,
        heirs(&[(alice, 10_000)]),
        None,
    );

    assert_error(
        send(&mut svm, &owner, instruction),
        DmsError::InvalidTimeout,
    );
}

#[test]
fn rejects_a_negative_timeout() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();

    let instruction =
        initialize_vault_ix(&owner.pubkey(), 1, -DAY, heirs(&[(alice, 10_000)]), None);

    assert_error(
        send(&mut svm, &owner, instruction),
        DmsError::InvalidTimeout,
    );
}

#[test]
fn one_owner_can_run_several_independent_vaults() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();
    let bob = Keypair::new().pubkey();

    let first = initialize_vault_ix(&owner.pubkey(), 1, 7 * DAY, heirs(&[(alice, 10_000)]), None);
    send(&mut svm, &owner, first).expect("first vault failed");

    let second = initialize_vault_ix(&owner.pubkey(), 2, 90 * DAY, heirs(&[(bob, 10_000)]), None);
    send(&mut svm, &owner, second).expect("second vault failed");

    let one = read_vault(&svm, &vault_pda(&owner.pubkey(), 1));
    let two = read_vault(&svm, &vault_pda(&owner.pubkey(), 2));

    assert_ne!(vault_pda(&owner.pubkey(), 1), vault_pda(&owner.pubkey(), 2));
    assert_eq!(one.timeout_seconds, 7 * DAY);
    assert_eq!(two.timeout_seconds, 90 * DAY);
    assert_eq!(one.active()[0].address, alice);
    assert_eq!(two.active()[0].address, bob);
}

#[test]
fn the_same_vault_cannot_be_opened_twice() {
    let (mut svm, owner) = setup();
    let alice = Keypair::new().pubkey();

    let first = initialize_vault_ix(&owner.pubkey(), 1, 7 * DAY, heirs(&[(alice, 10_000)]), None);
    send(&mut svm, &owner, first).expect("first vault failed");

    let again = initialize_vault_ix(&owner.pubkey(), 1, 7 * DAY, heirs(&[(alice, 10_000)]), None);
    assert!(
        send(&mut svm, &owner, again).is_err(),
        "re-initializing an existing vault must fail"
    );
}

#[test]
fn the_vault_account_is_rent_exempt_on_creation() {
    let (mut svm, owner) = setup();
    let (vault_key, _heir) = open_sol_vault(&mut svm, &owner, 1, 30 * DAY);

    let account = svm.get_account(&vault_key).unwrap();
    assert_eq!(account.owner, program_id());
    assert_eq!(account.data.len(), 8 + Vault::INIT_SPACE);
    assert!(
        account.lamports >= svm.minimum_balance_for_rent_exemption(account.data.len()),
        "a fresh vault must be rent-exempt"
    );
}
