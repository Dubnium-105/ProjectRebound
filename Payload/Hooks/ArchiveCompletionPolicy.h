#pragma once

// The native archive task can retain its initial HTTP-style pending sentinel
// after MetaServer has already persisted an otherwise successful update.
// Only that observed stale sentinel is compatible with a local success result;
// every real completion code must keep its native meaning.
namespace ArchiveCompletionPolicy
{
    inline constexpr int kStalePendingCode = 404;

    inline constexpr int NormalizePersistedCompletion(int completionCode)
    {
        return completionCode == kStalePendingCode ? 0 : completionCode;
    }
}
