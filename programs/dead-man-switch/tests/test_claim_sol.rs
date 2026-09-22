mod common;

use common::*;
use dead_man_switch::error::DmsError;
use solana_keypair::Keypair;
use solana_signer::Signer;

/// A transaction fee is a few thousand lamports; assertions on an heir's own
/// balance allow for it, while assertions on the vault stay exact.
const FEE_SLACK: u64 = 100_000;

#[test]
fn heirs_split_the_pool_by_their_shares() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[7_000, 3_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 10 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);

    let alice = &fixture.heirs[0];
    let alice_before = lamports(&svm, &alice.pubkey());
    send(
        &mut svm,
        alice,
        claim_sol_ix(&alice.pubkey(), &fixture.vault),
    )
    .expect("alice's claim failed");

    assert_eq!(vault_available(&svm, &fixture.vault), 3 * ONE_SOL);
    let alice_gain = lamports(&svm, &alice.pubkey()) - alice_before;
    assert!(alice_gain > 7 * ONE_SOL - FEE_SLACK && alice_gain <= 7 * ONE_SOL);

    let bob = &fixture.heirs[1];
    let bob_before = lamports(&svm, &bob.pubkey());
    send(&mut svm, bob, claim_sol_ix(&bob.pubkey(), &fixture.vault)).expect("bob's claim failed");

    assert_eq!(vault_available(&svm, &fixture.vault), 0);
    let bob_gain = lamports(&svm, &bob.pubkey()) - bob_before;
    assert!(bob_gain > 3 * ONE_SOL - FEE_SLACK && bob_gain <= 3 * ONE_SOL);

    let vault = read_vault(&svm, &fixture.vault);
    assert!(vault.is_claimed);
    assert_eq!(vault.claim_pool, 10 * ONE_SOL);
    assert!(vault.active().iter().all(|heir| heir.claimed));
}

/// The regression test for `claim_pool`.
///
/// Without the snapshot the second and third heirs would take their percentage
/// of whatever was left rather than of what the vault held.
#[test]
fn the_split_is_measured_against_a_snapshot_not_the_remaining_balance() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[5_000, 3_000, 2_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 100 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);

    // 50% of 100 → 50 left
    send(
        &mut svm,
        &fixture.heirs[0],
        claim_sol_ix(&fixture.heirs[0].pubkey(), &fixture.vault),
    )
    .expect("claim 1 failed");
    assert_eq!(vault_available(&svm, &fixture.vault), 50 * ONE_SOL);

    // 30% of the *original* 100 → 20 left. Of the remaining 50 it would have
    // been 15, leaving 35.
    send(
        &mut svm,
        &fixture.heirs[1],
        claim_sol_ix(&fixture.heirs[1].pubkey(), &fixture.vault),
    )
    .expect("claim 2 failed");
    assert_eq!(vault_available(&svm, &fixture.vault), 20 * ONE_SOL);

    send(
        &mut svm,
        &fixture.heirs[2],
        claim_sol_ix(&fixture.heirs[2].pubkey(), &fixture.vault),
    )
    .expect("claim 3 failed");
    assert_eq!(vault_available(&svm, &fixture.vault), 0);
}

#[test]
fn the_last_heir_sweeps_what_integer_division_left_behind() {
    let (mut svm, owner) = setup();
    // Thirds do not divide evenly into basis points, so a plain
    // share-of-pool calculation would strand a few lamports forever.
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[3_333, 3_333, 3_334], None);
    let deposit = 10 * ONE_SOL + 7;
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, deposit),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);
    for heir in &fixture.heirs {
        send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)).expect("claim failed");
    }

    assert_eq!(
        vault_available(&svm, &fixture.vault),
        0,
        "not a single lamport may be left stranded"
    );
}

#[test]
fn claiming_before_the_deadline_fails() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 5 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 30 * DAY - 1);

    let heir = &fixture.heirs[0];
    assert_error(
        send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)),
        DmsError::TimeoutNotReached,
    );
}

#[test]
fn claiming_the_very_second_the_deadline_lands_works() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 5 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 30 * DAY);

    let heir = &fixture.heirs[0];
    send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault))
        .expect("the deadline is inclusive");
    assert_eq!(vault_available(&svm, &fixture.vault), 0);
}

#[test]
fn a_stranger_cannot_claim() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 5 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);
    let stranger = funded_wallet(&mut svm);

    assert_error(
        send(
            &mut svm,
            &stranger,
            claim_sol_ix(&stranger.pubkey(), &fixture.vault),
        ),
        DmsError::Unauthorized,
    );
}

#[test]
fn an_heir_cannot_claim_twice() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[6_000, 4_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 10 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);
    let heir = &fixture.heirs[0];
    send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)).expect("first claim failed");

    assert_error(
        send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)),
        DmsError::AlreadyClaimed,
    );
}

#[test]
fn one_heir_claiming_does_not_block_the_others() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[5_000, 5_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 8 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);
    send(
        &mut svm,
        &fixture.heirs[0],
        claim_sol_ix(&fixture.heirs[0].pubkey(), &fixture.vault),
    )
    .expect("first claim failed");

    // A year later the second heir finally turns up — nothing expires.
    advance_clock(&mut svm, 365 * DAY);
    send(
        &mut svm,
        &fixture.heirs[1],
        claim_sol_ix(&fixture.heirs[1].pubkey(), &fixture.vault),
    )
    .expect("a late second claim must still work");

    assert_eq!(vault_available(&svm, &fixture.vault), 0);
}

#[test]
fn the_owner_is_frozen_out_once_the_first_claim_lands() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[5_000, 5_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 10 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);
    send(
        &mut svm,
        &fixture.heirs[0],
        claim_sol_ix(&fixture.heirs[0].pubkey(), &fixture.vault),
    )
    .expect("claim failed");

    assert_error(
        send(
            &mut svm,
            &owner,
            withdraw_sol_ix(&owner.pubkey(), &fixture.vault, ONE_SOL),
        ),
        DmsError::VaultTriggered,
    );
    assert_error(
        send(
            &mut svm,
            &owner,
            deposit_sol_ix(&owner.pubkey(), &fixture.vault, ONE_SOL),
        ),
        DmsError::VaultTriggered,
    );
    assert_error(
        send(
            &mut svm,
            &owner,
            set_beneficiaries_ix(
                &owner.pubkey(),
                &fixture.vault,
                heirs(&[(Keypair::new().pubkey(), 10_000)]),
            ),
        ),
        DmsError::VaultTriggered,
    );
}

#[test]
fn an_empty_vault_has_nothing_to_claim() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    advance_clock(&mut svm, 31 * DAY);
    let heir = &fixture.heirs[0];

    assert_error(
        send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)),
        DmsError::InsufficientFunds,
    );
}
