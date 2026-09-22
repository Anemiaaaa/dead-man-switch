mod common;

use common::*;
use dead_man_switch::error::DmsError;
use solana_signer::Signer;

#[test]
fn moves_lamports_into_the_vault() {
    let (mut svm, owner) = setup();
    let (vault_key, _heir) = open_sol_vault(&mut svm, &owner, 1, 30 * DAY);

    let rent_reserve = lamports(&svm, &vault_key);
    let owner_before = lamports(&svm, &owner.pubkey());

    let instruction = deposit_sol_ix(&owner.pubkey(), &vault_key, 5 * ONE_SOL);
    send(&mut svm, &owner, instruction).expect("deposit_sol failed");

    assert_eq!(lamports(&svm, &vault_key), rent_reserve + 5 * ONE_SOL);
    // The owner paid the deposit plus a transaction fee.
    assert!(lamports(&svm, &owner.pubkey()) < owner_before - 5 * ONE_SOL);
}

#[test]
fn deposits_accumulate() {
    let (mut svm, owner) = setup();
    let (vault_key, _heir) = open_sol_vault(&mut svm, &owner, 1, 30 * DAY);
    let rent_reserve = lamports(&svm, &vault_key);

    for _ in 0..3 {
        let instruction = deposit_sol_ix(&owner.pubkey(), &vault_key, 2 * ONE_SOL);
        send(&mut svm, &owner, instruction).expect("deposit_sol failed");
    }

    assert_eq!(lamports(&svm, &vault_key), rent_reserve + 6 * ONE_SOL);
}

#[test]
fn a_deposit_never_touches_the_rent_reserve() {
    let (mut svm, owner) = setup();
    let (vault_key, _heir) = open_sol_vault(&mut svm, &owner, 1, 30 * DAY);

    let instruction = deposit_sol_ix(&owner.pubkey(), &vault_key, 3 * ONE_SOL);
    send(&mut svm, &owner, instruction).expect("deposit_sol failed");

    let account = svm.get_account(&vault_key).unwrap();
    let reserve = svm.minimum_balance_for_rent_exemption(account.data.len());

    // Everything above the reserve — and only that — is what heirs can claim.
    assert_eq!(account.lamports - reserve, 3 * ONE_SOL);
}

#[test]
fn rejects_a_zero_amount() {
    let (mut svm, owner) = setup();
    let (vault_key, _heir) = open_sol_vault(&mut svm, &owner, 1, 30 * DAY);

    let instruction = deposit_sol_ix(&owner.pubkey(), &vault_key, 0);

    assert_error(send(&mut svm, &owner, instruction), DmsError::InvalidAmount);
}

#[test]
fn rejects_a_deposit_from_anyone_but_the_owner() {
    let (mut svm, owner) = setup();
    let (vault_key, _heir) = open_sol_vault(&mut svm, &owner, 1, 30 * DAY);
    let stranger = funded_wallet(&mut svm);

    // The stranger signs and pays, but the vault still belongs to `owner`.
    let instruction = deposit_sol_ix(&stranger.pubkey(), &vault_key, ONE_SOL);

    assert_error(
        send(&mut svm, &stranger, instruction),
        DmsError::Unauthorized,
    );
}

#[test]
fn deposits_go_to_the_vault_the_id_points_at() {
    let (mut svm, owner) = setup();
    let (first, _) = open_sol_vault(&mut svm, &owner, 1, 30 * DAY);
    let (second, _) = open_sol_vault(&mut svm, &owner, 2, 30 * DAY);

    let second_before = lamports(&svm, &second);

    let instruction = deposit_sol_ix(&owner.pubkey(), &first, 4 * ONE_SOL);
    send(&mut svm, &owner, instruction).expect("deposit_sol failed");

    assert_eq!(
        lamports(&svm, &second),
        second_before,
        "a deposit must not leak into a sibling vault"
    );
}
