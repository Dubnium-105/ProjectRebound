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
}
