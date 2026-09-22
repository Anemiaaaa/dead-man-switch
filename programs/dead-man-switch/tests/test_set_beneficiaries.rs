mod common;

use common::*;
use dead_man_switch::error::DmsError;
use solana_keypair::Keypair;
use solana_signer::Signer;

#[test]
fn replaces_the_whole_table() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    let carol = Keypair::new().pubkey();
    let dave = Keypair::new().pubkey();
    send(
        &mut svm,
        &owner,
        set_beneficiaries_ix(
            &owner.pubkey(),
            &fixture.vault,
            heirs(&[(carol, 6_000), (dave, 4_000)]),
        ),
    )
    .expect("set_beneficiaries failed");

    let vault = read_vault(&svm, &fixture.vault);
    assert_eq!(vault.beneficiary_count, 2);
    assert_eq!(vault.active()[0].address, carol);
    assert_eq!(vault.active()[0].share_bps, 6_000);
    assert_eq!(vault.active()[1].address, dave);
    assert_eq!(vault.active()[1].share_bps, 4_000);
}

#[test]
fn shrinking_the_table_leaves_no_ghosts_behind() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[4_000, 3_000, 3_000], None);

    let solo = Keypair::new().pubkey();
    send(
        &mut svm,
        &owner,
        set_beneficiaries_ix(&owner.pubkey(), &fixture.vault, heirs(&[(solo, 10_000)])),
    )
    .expect("set_beneficiaries failed");

    let vault = read_vault(&svm, &fixture.vault);
    assert_eq!(vault.beneficiary_count, 1);
    assert_eq!(vault.active().len(), 1);
    // The dropped heirs are gone from the table, not merely hidden past the
    // count.
    assert!(vault.index_of(&fixture.heirs[1].pubkey()).is_none());
    assert_eq!(vault.beneficiaries[1], Default::default());
    assert_eq!(vault.beneficiaries[2], Default::default());
}

#[test]
fn a_dropped_heir_can_no_longer_claim() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[5_000, 5_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 10 * ONE_SOL),
    )
    .expect("deposit failed");

    let kept = &fixture.heirs[0];
    let dropped = &fixture.heirs[1];
    send(
        &mut svm,
        &owner,
        set_beneficiaries_ix(
            &owner.pubkey(),
            &fixture.vault,
            heirs(&[(kept.pubkey(), 10_000)]),
        ),
    )
    .expect("set_beneficiaries failed");

    advance_clock(&mut svm, 31 * DAY);

    assert_error(
        send(
            &mut svm,
            dropped,
            claim_sol_ix(&dropped.pubkey(), &fixture.vault),
        ),
        DmsError::Unauthorized,
    );
    send(&mut svm, kept, claim_sol_ix(&kept.pubkey(), &fixture.vault))
        .expect("the remaining heir takes everything");
    assert_eq!(vault_available(&svm, &fixture.vault), 0);
}

#[test]
fn rejects_a_table_that_does_not_total_one_hundred_percent() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    assert_error(
        send(
            &mut svm,
            &owner,
            set_beneficiaries_ix(
                &owner.pubkey(),
                &fixture.vault,
                heirs(&[
                    (Keypair::new().pubkey(), 6_000),
                    (Keypair::new().pubkey(), 3_000),
                ]),
            ),
        ),
        DmsError::InvalidShares,
    );
}

#[test]
fn rejects_an_empty_table() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    assert_error(
        send(
            &mut svm,
            &owner,
            set_beneficiaries_ix(&owner.pubkey(), &fixture.vault, vec![]),
        ),
        DmsError::NoBeneficiaries,
    );
}

#[test]
fn rejects_more_heirs_than_the_vault_can_hold() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    let six: Vec<_> = (0..6)
        .map(|i| {
            (
                Keypair::new().pubkey(),
                if i == 0 { 5_000u16 } else { 1_000u16 },
            )
        })
        .collect();

    assert_error(
        send(
            &mut svm,
            &owner,
            set_beneficiaries_ix(&owner.pubkey(), &fixture.vault, heirs(&six)),
        ),
        DmsError::TooManyBeneficiaries,
    );
}

#[test]
fn rejects_a_duplicate_heir() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    let twice = Keypair::new().pubkey();

    assert_error(
        send(
            &mut svm,
            &owner,
            set_beneficiaries_ix(
                &owner.pubkey(),
                &fixture.vault,
                heirs(&[(twice, 5_000), (twice, 5_000)]),
            ),
        ),
        DmsError::DuplicateBeneficiary,
    );
}

#[test]
fn rejects_edits_from_anyone_but_the_owner() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    let heir = &fixture.heirs[0];

    // An heir writing themselves a bigger share would be the whole ballgame.
    assert_error(
        send(
            &mut svm,
            heir,
            set_beneficiaries_ix(
                &heir.pubkey(),
                &fixture.vault,
                heirs(&[(heir.pubkey(), 10_000)]),
            ),
        ),
        DmsError::Unauthorized,
    );
}

#[test]
fn editing_the_table_does_not_secretly_reset_the_timer() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    advance_clock(&mut svm, 10 * DAY);
    send(
        &mut svm,
        &owner,
        set_beneficiaries_ix(
            &owner.pubkey(),
            &fixture.vault,
            heirs(&[(Keypair::new().pubkey(), 10_000)]),
        ),
    )
    .expect("set_beneficiaries failed");

    // Only `check_in` moves the deadline; every other instruction is
    // single-purpose.
    assert_eq!(
        read_vault(&svm, &fixture.vault).last_checkin,
        GENESIS,
        "editing heirs must not count as a check-in"
    );
}
