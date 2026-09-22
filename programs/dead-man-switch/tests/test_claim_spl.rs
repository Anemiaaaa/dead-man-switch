mod common;

use common::*;
use dead_man_switch::error::DmsError;
use solana_keypair::Keypair;
use solana_signer::Signer;

const MILLION: u64 = 1_000_000;

/// Opens a funded SPL vault and returns the mint alongside it.
fn funded_spl_vault(
    svm: &mut litesvm::LiteSVM,
    owner: &Keypair,
    shares: &[u16],
    deposit: u64,
) -> (VaultFixture, SplFixture) {
    let spl = mint_with_balance(svm, owner, 100 * MILLION);
    let fixture = open_vault_with(svm, owner, 1, 30 * DAY, shares, Some(spl.mint));
    send(
        svm,
        owner,
        deposit_spl_ix(&owner.pubkey(), &fixture.vault, &spl.mint, deposit),
    )
    .expect("deposit_spl failed");
    (fixture, spl)
}

#[test]
fn heirs_split_the_token_pool_by_their_shares() {
    let (mut svm, owner) = setup();
    let (fixture, spl) = funded_spl_vault(&mut svm, &owner, &[7_000, 3_000], 10 * MILLION);

    advance_clock(&mut svm, 31 * DAY);

    let alice = &fixture.heirs[0];
    send(
        &mut svm,
        alice,
        claim_spl_ix(&alice.pubkey(), &fixture.vault, &spl.mint),
    )
    .expect("alice's claim failed");

    assert_eq!(
        token_balance(&svm, &ata(&alice.pubkey(), &spl.mint)),
        7 * MILLION
    );
    assert_eq!(
        token_balance(&svm, &ata(&fixture.vault, &spl.mint)),
        3 * MILLION
    );

    let bob = &fixture.heirs[1];
    send(
        &mut svm,
        bob,
        claim_spl_ix(&bob.pubkey(), &fixture.vault, &spl.mint),
    )
    .expect("bob's claim failed");

    assert_eq!(
        token_balance(&svm, &ata(&bob.pubkey(), &spl.mint)),
        3 * MILLION
    );
    assert_eq!(token_balance(&svm, &ata(&fixture.vault, &spl.mint)), 0);
}

#[test]
fn an_heir_who_never_held_the_token_still_gets_paid() {
    let (mut svm, owner) = setup();
    let (fixture, spl) = funded_spl_vault(&mut svm, &owner, &[10_000], 4 * MILLION);

    let heir = &fixture.heirs[0];
    let heir_ata = ata(&heir.pubkey(), &spl.mint);
    assert!(
        svm.get_account(&heir_ata).is_none() || svm.get_account(&heir_ata).unwrap().data.is_empty(),
        "the heir should not have a token account yet"
    );

    advance_clock(&mut svm, 31 * DAY);
    send(
        &mut svm,
        heir,
        claim_spl_ix(&heir.pubkey(), &fixture.vault, &spl.mint),
    )
    .expect("claim should create the heir's token account");

    assert_eq!(token_balance(&svm, &heir_ata), 4 * MILLION);
}

#[test]
fn the_last_heir_sweeps_the_remainder() {
    let (mut svm, owner) = setup();
    let (fixture, spl) =
        funded_spl_vault(&mut svm, &owner, &[3_333, 3_333, 3_334], 10 * MILLION + 7);

    advance_clock(&mut svm, 31 * DAY);
    for heir in &fixture.heirs {
        send(
            &mut svm,
            heir,
            claim_spl_ix(&heir.pubkey(), &fixture.vault, &spl.mint),
        )
        .expect("claim failed");
    }

    assert_eq!(token_balance(&svm, &ata(&fixture.vault, &spl.mint)), 0);
}

#[test]
fn claiming_before_the_deadline_fails() {
    let (mut svm, owner) = setup();
    let (fixture, spl) = funded_spl_vault(&mut svm, &owner, &[10_000], 5 * MILLION);

    advance_clock(&mut svm, 30 * DAY - 1);
    let heir = &fixture.heirs[0];

    assert_error(
        send(
            &mut svm,
            heir,
            claim_spl_ix(&heir.pubkey(), &fixture.vault, &spl.mint),
        ),
        DmsError::TimeoutNotReached,
    );
}

#[test]
fn a_stranger_cannot_claim() {
    let (mut svm, owner) = setup();
    let (fixture, spl) = funded_spl_vault(&mut svm, &owner, &[10_000], 5 * MILLION);

    advance_clock(&mut svm, 31 * DAY);
    let stranger = funded_wallet(&mut svm);

    assert_error(
        send(
            &mut svm,
            &stranger,
            claim_spl_ix(&stranger.pubkey(), &fixture.vault, &spl.mint),
        ),
        DmsError::Unauthorized,
    );
}

#[test]
fn an_heir_cannot_claim_twice() {
    let (mut svm, owner) = setup();
    let (fixture, spl) = funded_spl_vault(&mut svm, &owner, &[6_000, 4_000], 10 * MILLION);

    advance_clock(&mut svm, 31 * DAY);
    let heir = &fixture.heirs[0];
    send(
        &mut svm,
        heir,
        claim_spl_ix(&heir.pubkey(), &fixture.vault, &spl.mint),
    )
    .expect("first claim failed");

    assert_error(
        send(
            &mut svm,
            heir,
            claim_spl_ix(&heir.pubkey(), &fixture.vault, &spl.mint),
        ),
        DmsError::AlreadyClaimed,
    );
}

#[test]
fn a_sol_claim_cannot_drain_a_token_vault() {
    let (mut svm, owner) = setup();
    let (fixture, _spl) = funded_spl_vault(&mut svm, &owner, &[10_000], 5 * MILLION);

    advance_clock(&mut svm, 31 * DAY);
    let heir = &fixture.heirs[0];

    // The SOL path would otherwise hand out the vault's rent reserve.
    assert_error(
        send(&mut svm, heir, claim_sol_ix(&heir.pubkey(), &fixture.vault)),
        DmsError::NotSolVault,
    );
}
