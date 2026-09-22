mod common;

use common::*;
use dead_man_switch::error::DmsError;
use solana_signer::Signer;

const MILLION: u64 = 1_000_000;

#[test]
fn closes_an_empty_vault_and_returns_the_rent() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);

    let rent = lamports(&svm, &fixture.vault);
    let owner_before = lamports(&svm, &owner.pubkey());

    send(
        &mut svm,
        &owner,
        close_vault_ix(&owner.pubkey(), &fixture.vault, None),
    )
    .expect("close_vault failed");

    assert!(
        svm.get_account(&fixture.vault)
            .is_none_or(|account| account.data.is_empty()),
        "the vault account should be gone"
    );
    assert!(lamports(&svm, &owner.pubkey()) > owner_before + rent - 100_000);
}

#[test]
fn refuses_to_close_a_vault_that_still_holds_funds() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, ONE_SOL),
    )
    .expect("deposit failed");

    assert_error(
        send(
            &mut svm,
            &owner,
            close_vault_ix(&owner.pubkey(), &fixture.vault, None),
        ),
        DmsError::VaultNotEmpty,
    );
}

#[test]
fn closes_once_every_heir_has_claimed() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[6_000, 4_000], None);
    send(
        &mut svm,
        &owner,
        deposit_sol_ix(&owner.pubkey(), &fixture.vault, 10 * ONE_SOL),
    )
    .expect("deposit failed");

    advance_clock(&mut svm, 31 * DAY);
    for heir in &fixture.heirs {
        send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)).expect("claim failed");
    }

    // The rent was the owner's money all along, so it comes back to them even
    // though the switch has tripped.
    send(
        &mut svm,
        &owner,
        close_vault_ix(&owner.pubkey(), &fixture.vault, None),
    )
    .expect("close_vault failed");
}

#[test]
fn rejects_a_close_from_anyone_but_the_owner() {
    let (mut svm, owner) = setup();
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], None);
    let heir = &fixture.heirs[0];

    assert_error(
        send(
            &mut svm,
            heir,
            close_vault_ix(&heir.pubkey(), &fixture.vault, None),
        ),
        DmsError::Unauthorized,
    );
}

#[test]
fn closes_an_spl_vault_along_with_its_token_account() {
    let (mut svm, owner) = setup();
    let spl = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], Some(spl.mint));
    send(
        &mut svm,
        &owner,
        deposit_spl_ix(&owner.pubkey(), &fixture.vault, &spl.mint, 5 * MILLION),
    )
    .expect("deposit_spl failed");
    send(
        &mut svm,
        &owner,
        withdraw_spl_ix(&owner.pubkey(), &fixture.vault, &spl.mint, 5 * MILLION),
    )
    .expect("withdraw_spl failed");

    let vault_ata = ata(&fixture.vault, &spl.mint);
    send(
        &mut svm,
        &owner,
        close_vault_ix(&owner.pubkey(), &fixture.vault, Some(spl.mint)),
    )
    .expect("close_vault failed");

    assert!(
        svm.get_account(&vault_ata)
            .is_none_or(|account| account.data.is_empty()),
        "the vault's token account should be closed too, not left holding rent"
    );
}

#[test]
fn refuses_to_close_an_spl_vault_that_still_holds_tokens() {
    let (mut svm, owner) = setup();
    let spl = mint_with_balance(&mut svm, &owner, 10 * MILLION);
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
            close_vault_ix(&owner.pubkey(), &fixture.vault, Some(spl.mint)),
        ),
        DmsError::VaultNotEmpty,
    );
}

#[test]
fn refuses_to_close_an_spl_vault_without_its_token_account() {
    let (mut svm, owner) = setup();
    let spl = mint_with_balance(&mut svm, &owner, 10 * MILLION);
    let fixture = open_vault_with(&mut svm, &owner, 1, 30 * DAY, &[10_000], Some(spl.mint));

    // Omitting the token account would otherwise close the vault and strand
    // its ATA with nobody left who can sign for it.
    assert_error(
        send(
            &mut svm,
            &owner,
            close_vault_ix(&owner.pubkey(), &fixture.vault, None),
        ),
        DmsError::MissingTokenAccount,
    );
}
