mod common;

use common::*;
use dead_man_switch::error::DmsError;
use solana_signer::Signer;

#[test]
fn pushes_the_deadline_out_by_a_full_timeout() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    assert_eq!(
        read_vault(&svm, &fixture.vault).deadline().unwrap(),
        GENESIS + 30 * DAY
    );

    advance_clock(&mut svm, 20 * DAY);
    send(
        &mut svm,
        &owner,
        check_in_ix(&owner.pubkey(), &fixture.vault),
    )
    .expect("check_in failed");

    let vault = read_vault(&svm, &fixture.vault);
    assert_eq!(vault.last_checkin, GENESIS + 20 * DAY);
    assert_eq!(vault.deadline().unwrap(), GENESIS + 50 * DAY);
}

#[test]
fn a_check_in_keeps_heirs_locked_out() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 5 * ONE_SOL),
    )
    .expect("deposit failed");

    // One day short of the deadline the owner checks in...
    advance_clock(&mut svm, 29 * DAY);
    send(
        &mut svm,
        &owner,
        check_in_ix(&owner.pubkey(), &fixture.vault),
    )
    .expect("check_in failed");

    // ...so the original deadline passing means nothing any more.
    advance_clock(&mut svm, 2 * DAY);
    let heir = &fixture.heirs[0];
    assert_error(
        send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)),
        DmsError::TimeoutNotReached,
    );
}

#[test]
fn rejects_a_check_in_from_anyone_but_the_owner() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    let heir = &fixture.heirs[0];

    // Not even a beneficiary may reset the timer — that would let one heir
    // stall the others indefinitely.
    assert_error(
        send(&mut svm, heir, check_in_ix(&heir.pubkey(), &fixture.vault)),
        DmsError::Unauthorized,
    );
}

#[test]
fn rejects_a_check_in_after_the_switch_has_tripped() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[7_000, 3_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 10 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);
    let first_heir = &fixture.heirs[0];
    send(
        &mut svm,
        first_heir,
        claim_sol_ix(&first_heir.pubkey(), &fixture.vault),
    )
    .expect("claim failed");

    // Once someone has actually inherited, the owner cannot un-ring the bell.
    assert_error(
        send(
            &mut svm,
            &owner,
            check_in_ix(&owner.pubkey(), &fixture.vault),
        ),
        DmsError::VaultTriggered,
    );
}

#[test]
fn an_owner_who_is_merely_late_can_still_check_in() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    // Past the deadline, but no heir has moved yet: being late is not the same
    // as being dead.
    advance_clock(&mut svm, 40 * DAY);
    send(
        &mut svm,
        &owner,
        check_in_ix(&owner.pubkey(), &fixture.vault),
    )
    .expect("a late check-in should still work before any claim");

    let heir = &fixture.heirs[0];
    assert_error(
        send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)),
        DmsError::TimeoutNotReached,
    );
}
