#pragma once

#include <cstdint>
#include <span>
#include <string_view>

namespace StrictRoster
{
    // Verifies an RFC 8032 Ed25519 signature over the original message.
    // The implementation is self-contained apart from Windows CNG SHA-512;
    // it never accepts pre-hashed input or a non-canonical S scalar.
    [[nodiscard]] bool VerifyEd25519(
        std::span<const std::uint8_t> publicKey,
        std::string_view message,
        std::span<const std::uint8_t> signature) noexcept;
}
