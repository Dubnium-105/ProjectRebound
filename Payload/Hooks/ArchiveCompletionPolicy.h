#pragma once

// Native archive tasks can retain a transport/completion sentinel after
// MetaServer has already persisted an otherwise successful update. Keep the
// compatibility surface path-specific: the equipment archive dispatcher has
// one additional observed persisted result that the other archive callbacks
// must continue to treat normally.
namespace ArchiveCompletionPolicy
{
    inline constexpr int kStalePendingCode = 404;
    inline constexpr int kEquipmentPersistedCode = 0x232A;

    inline constexpr int NormalizePersistedCompletion(int completionCode)
    {
        return completionCode == kStalePendingCode ? 0 : completionCode;
    }

    inline constexpr int NormalizeEquipmentCompletion(int completionCode)
    {
        return completionCode == kEquipmentPersistedCode
            ? 0
            : NormalizePersistedCompletion(completionCode);
    }

    // Weapon customization updates use four different native completion
    // entry points (part slot, suite/skin, part skin/painting and ornament),
    // but share the generic persisted-update status contract. Keep a named
    // policy here so those callbacks cannot accidentally inherit the
    // equipment-only 9002 compatibility rule.
    inline constexpr int NormalizeWeaponCustomizationCompletion(int completionCode)
    {
        return NormalizePersistedCompletion(completionCode);
    }
}
