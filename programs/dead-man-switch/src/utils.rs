use anchor_lang::prelude::*;

use crate::error::DmsError;

/// Moves lamports directly between two accounts in this instruction.
///
/// A System Program transfer cannot do this job: its source must be a
/// System-owned account with no data, and the vault PDA is owned by this
/// program and carries state. Mutating the lamport balances directly is the
/// supported way for a program to move funds out of an account it owns — the
/// runtime still checks that the totals balance at the end of the instruction.
pub fn move_lamports<'info>(
    from: &AccountInfo<'info>,
    to: &AccountInfo<'info>,
    amount: u64,
) -> Result<()> {
    let debited = from
        .lamports()
        .checked_sub(amount)
        .ok_or_else(|| error!(DmsError::InsufficientFunds))?;
    let credited = to
        .lamports()
        .checked_add(amount)
        .ok_or_else(|| error!(DmsError::MathOverflow))?;

    **from.try_borrow_mut_lamports()? = debited;
    **to.try_borrow_mut_lamports()? = credited;

    Ok(())
}
