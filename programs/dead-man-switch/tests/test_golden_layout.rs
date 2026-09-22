//! Cross-language layout guard.
//!
//! The Go keeper decodes `Vault` accounts by hand, with no generated bindings
//! in between. These golden files are the contract: this test asserts that the
//! program still produces those exact bytes, and
//! `keeper/internal/dms/golden_test.go` asserts that the keeper still reads
//! them back correctly.
//!
//! Change the account layout and both sides fail, which is the point — a
//! silent drift here would mean a keeper quietly reporting wrong deadlines.
//!
//! Regenerate after an intentional layout change:
//!
//! ```text
//! UPDATE_GOLDEN=1 cargo test --test test_golden_layout
//! ```

use std::{fs, path::PathBuf};

use anchor_lang::{prelude::Pubkey, AccountSerialize, Space};
use dead_man_switch::state::{Beneficiary, Vault};

fn golden_path(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../keeper/testdata")
        .join(name)
}

/// Serializes a vault exactly as the runtime stores it: discriminator, fields,
/// then zero padding out to the allocated size.
fn serialize(vault: &Vault) -> Vec<u8> {
    let mut data = Vec::new();
    vault
        .try_serialize(&mut data)
        .expect("serializing the vault");
    assert!(
        data.len() <= 8 + Vault::INIT_SPACE,
        "a vault serialized to {} bytes, more than the {} allocated",
        data.len(),
        8 + Vault::INIT_SPACE
    );
    data.resize(8 + Vault::INIT_SPACE, 0);
    data
}

fn assert_matches_golden(name: &str, bytes: &[u8]) {
    let path = golden_path(name);

    if std::env::var_os("UPDATE_GOLDEN").is_some() {
        fs::create_dir_all(path.parent().unwrap()).expect("creating testdata");
        fs::write(&path, bytes).expect("writing the golden file");
        return;
    }

    let expected = fs::read(&path).unwrap_or_else(|err| {
        panic!(
            "reading {}: {err}\nrun UPDATE_GOLDEN=1 cargo test to create it",
            path.display()
        )
    });

    assert_eq!(
        bytes,
        expected.as_slice(),
        "{} no longer matches the account layout — if that was intentional, \
         regenerate with UPDATE_GOLDEN=1 and update the keeper's decoder",
        path.display()
    );
}

/// Fixed keys, so the golden files are byte-stable across runs.
fn key(byte: u8) -> Pubkey {
    Pubkey::new_from_array([byte; 32])
}

fn heir(byte: u8, share_bps: u16, claimed: bool) -> Beneficiary {
    Beneficiary {
        address: key(byte),
        share_bps,
        claimed,
    }
}

#[test]
fn sol_vault_layout_is_stable() {
    let mut beneficiaries = [Beneficiary::default(); 5];
    beneficiaries[0] = heir(0xA1, 7_000, false);
    beneficiaries[1] = heir(0xB2, 3_000, false);

    let vault = Vault {
        owner: key(0x11),
        vault_id: 7,
        // `None` serializes to a single byte, shifting everything after it —
        // the exact case a fixed-offset decoder gets wrong.
        mint: None,
        last_checkin: 1_800_000_000,
        timeout_seconds: 30 * 24 * 60 * 60,
        beneficiaries,
        beneficiary_count: 2,
        is_claimed: false,
        claim_pool: 0,
        bump: 254,
    };

    assert_matches_golden("vault_sol.bin", &serialize(&vault));
}

#[test]
fn spl_vault_layout_is_stable() {
    let mut beneficiaries = [Beneficiary::default(); 5];
    beneficiaries[0] = heir(0xC3, 5_000, true);
    beneficiaries[1] = heir(0xD4, 3_000, false);
    beneficiaries[2] = heir(0xE5, 2_000, false);

    let vault = Vault {
        owner: key(0x22),
        vault_id: u64::MAX, // a vault id no signed 64-bit column could hold
        mint: Some(key(0x33)),
        last_checkin: 1_700_000_000,
        timeout_seconds: 7 * 24 * 60 * 60,
        beneficiaries,
        beneficiary_count: 3,
        is_claimed: true,
        claim_pool: 123_456_789_012,
        bump: 250,
    };

    assert_matches_golden("vault_spl.bin", &serialize(&vault));
}

#[test]
fn the_allocated_size_is_what_the_keeper_filters_on() {
    // The keeper asks the RPC node for accounts of exactly this size. If the
    // struct grows, that filter silently returns nothing at all.
    assert_eq!(8 + Vault::INIT_SPACE, 283);
}
