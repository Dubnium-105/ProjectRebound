#include "../Admission/Ed25519Verifier.h"

#include <algorithm>
#include <array>
#include <cstdint>
#include <cstdlib>
#include <iostream>
#include <span>
#include <string>
#include <string_view>

namespace
{
    [[noreturn]] void Fail(const std::string& message)
    {
        std::cerr << "FAIL: " << message << '\n';
        std::exit(1);
    }

    int HexValue(const char value)
    {
        if (value >= '0' && value <= '9') return value - '0';
        if (value >= 'a' && value <= 'f') return value - 'a' + 10;
        if (value >= 'A' && value <= 'F') return value - 'A' + 10;
        return -1;
    }

    template<std::size_t Size>
    std::array<std::uint8_t, Size> DecodeHex(const std::string_view value)
    {
        if (value.size() != Size * 2U)
            Fail("invalid test-vector size");
        std::array<std::uint8_t, Size> result{};
        for (std::size_t index = 0; index < Size; ++index)
        {
            const int high = HexValue(value[index * 2U]);
            const int low = HexValue(value[index * 2U + 1U]);
            if (high < 0 || low < 0)
                Fail("invalid test-vector hex");
            result[index] = static_cast<std::uint8_t>((high << 4) | low);
        }
        return result;
    }
}

int main()
{
    // RFC 8032, section 7.1, TEST 1 (empty message).
    const auto publicKey = DecodeHex<32>(
        "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a");
    auto signature = DecodeHex<64>(
        "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e06522490155"
        "5fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b");
    if (!StrictRoster::VerifyEd25519(publicKey, "", signature))
        Fail("valid RFC 8032 signature was rejected");

    signature[0] ^= 1U;
    if (StrictRoster::VerifyEd25519(publicKey, "", signature))
        Fail("tampered signature was accepted");
    signature[0] ^= 1U;

    // S == group order is non-canonical and must be rejected.
    constexpr std::array<std::uint8_t, 32> order{
        0xed, 0xd3, 0xf5, 0x5c, 0x1a, 0x63, 0x12, 0x58,
        0xd6, 0x9c, 0xf7, 0xa2, 0xde, 0xf9, 0xde, 0x14,
        0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
        0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10};
    std::copy(order.begin(), order.end(), signature.begin() + 32);
    if (StrictRoster::VerifyEd25519(publicKey, "", signature))
        Fail("non-canonical S scalar was accepted");

    std::cout << "Ed25519 verifier tests passed\n";
    return 0;
}
