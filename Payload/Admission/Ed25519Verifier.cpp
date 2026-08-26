#include "Ed25519Verifier.h"

#include <Windows.h>
#include <bcrypt.h>

#include <algorithm>
#include <array>
#include <cstddef>
#include <cstdint>
#include <limits>
#include <vector>

#pragma comment(lib, "Bcrypt.lib")

// The field/group operations below are derived from TweetNaCl's public-domain
// Ed25519 verification implementation. SHA-512 is delegated to Windows CNG.
namespace
{
    using Byte = std::uint8_t;
    using Limb = std::int64_t;
    using Field = std::array<Limb, 16>;
    using Point = std::array<Field, 4>;

    constexpr Field kZero{};
    constexpr Field kOne{1};
    constexpr Field kD{
        0x78a3, 0x1359, 0x4dca, 0x75eb, 0xd8ab, 0x4141, 0x0a4d, 0x0070,
        0xe898, 0x7779, 0x4079, 0x8cc7, 0xfe73, 0x2b6f, 0x6cee, 0x5203};
    constexpr Field kD2{
        0xf159, 0x26b2, 0x9b94, 0xebd6, 0xb156, 0x8283, 0x149a, 0x00e0,
        0xd130, 0xeef3, 0x80f2, 0x198e, 0xfce7, 0x56df, 0xd9dc, 0x2406};
    constexpr Field kX{
        0xd51a, 0x8f25, 0x2d60, 0xc956, 0xa7b2, 0x9525, 0xc760, 0x692c,
        0xdc5c, 0xfdd6, 0xe231, 0xc0a4, 0x53fe, 0xcd6e, 0x36d3, 0x2169};
    constexpr Field kY{
        0x6658, 0x6666, 0x6666, 0x6666, 0x6666, 0x6666, 0x6666, 0x6666,
        0x6666, 0x6666, 0x6666, 0x6666, 0x6666, 0x6666, 0x6666, 0x6666};
    constexpr Field kI{
        0xa0b0, 0x4a0e, 0x1b27, 0xc4ee, 0xe478, 0xad2f, 0x1806, 0x2f43,
        0xd7a7, 0x3dfb, 0x0099, 0x2b4d, 0xdf0b, 0x4fc1, 0x2480, 0x2b83};
    constexpr std::array<Limb, 32> kOrder{
        0xed, 0xd3, 0xf5, 0x5c, 0x1a, 0x63, 0x12, 0x58,
        0xd6, 0x9c, 0xf7, 0xa2, 0xde, 0xf9, 0xde, 0x14,
        0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
        0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10};

    [[nodiscard]] bool NtSuccess(const NTSTATUS status) noexcept
    {
        return status >= 0;
    }

    [[nodiscard]] bool Sha512(
        const std::span<const Byte> message,
        std::array<Byte, 64>& digest) noexcept
    {
        if (message.size() > (std::numeric_limits<ULONG>::max)())
            return false;

        BCRYPT_ALG_HANDLE algorithm = nullptr;
        BCRYPT_HASH_HANDLE hash = nullptr;
        DWORD objectLength = 0;
        DWORD returned = 0;
        std::vector<Byte> object;
        bool succeeded = false;
        if (!NtSuccess(BCryptOpenAlgorithmProvider(
                &algorithm, BCRYPT_SHA512_ALGORITHM, nullptr, 0)))
        {
            return false;
        }
        if (!NtSuccess(BCryptGetProperty(
                algorithm, BCRYPT_OBJECT_LENGTH,
                reinterpret_cast<PUCHAR>(&objectLength), sizeof(objectLength),
                &returned, 0)) || returned != sizeof(objectLength) || objectLength == 0)
        {
            BCryptCloseAlgorithmProvider(algorithm, 0);
            return false;
        }
        try
        {
            object.resize(objectLength);
            succeeded = NtSuccess(BCryptCreateHash(
                    algorithm, &hash, object.data(), objectLength,
                    nullptr, 0, 0)) &&
                NtSuccess(BCryptHashData(
                    hash, const_cast<PUCHAR>(message.data()),
                    static_cast<ULONG>(message.size()), 0)) &&
                NtSuccess(BCryptFinishHash(
                    hash, digest.data(), static_cast<ULONG>(digest.size()), 0));
        }
        catch (...)
        {
            succeeded = false;
        }
        if (hash != nullptr)
            BCryptDestroyHash(hash);
        if (!object.empty())
            SecureZeroMemory(object.data(), object.size());
        BCryptCloseAlgorithmProvider(algorithm, 0);
        return succeeded;
    }

    void Carry(Field& output) noexcept
    {
        for (std::size_t index = 0; index < output.size(); ++index)
        {
            output[index] += static_cast<Limb>(1) << 16;
            const Limb carry = output[index] >> 16;
            output[index] -= carry << 16;
            if (index < output.size() - 1U)
                output[index + 1U] += carry - 1;
            else
                output[0] += 38 * (carry - 1);
        }
    }

    void ConditionalSwap(Field& left, Field& right, const int choice) noexcept
    {
        const Limb mask = ~static_cast<Limb>(choice - 1);
        for (std::size_t index = 0; index < left.size(); ++index)
        {
            const Limb value = mask & (left[index] ^ right[index]);
            left[index] ^= value;
            right[index] ^= value;
        }
    }

    void PackField(std::span<Byte, 32> output, const Field& input) noexcept
    {
        Field reduced = input;
        Field candidate{};
        Carry(reduced);
        Carry(reduced);
        Carry(reduced);
        for (int pass = 0; pass < 2; ++pass)
        {
            candidate[0] = reduced[0] - 0xffed;
            for (std::size_t index = 1; index < 15U; ++index)
            {
                candidate[index] = reduced[index] - 0xffff -
                    ((candidate[index - 1U] >> 16) & 1);
                candidate[index - 1U] &= 0xffff;
            }
            candidate[15] = reduced[15] - 0x7fff -
                ((candidate[14] >> 16) & 1);
            const int borrow = static_cast<int>((candidate[15] >> 16) & 1);
            candidate[14] &= 0xffff;
            ConditionalSwap(reduced, candidate, 1 - borrow);
        }
        for (std::size_t index = 0; index < 16U; ++index)
        {
            output[index * 2U] = static_cast<Byte>(reduced[index] & 0xff);
            output[index * 2U + 1U] = static_cast<Byte>(reduced[index] >> 8);
        }
    }

    [[nodiscard]] bool EqualBytes(
        const std::span<const Byte> left,
        const std::span<const Byte> right) noexcept
    {
        if (left.size() != right.size())
            return false;
        unsigned int difference = 0;
        for (std::size_t index = 0; index < left.size(); ++index)
            difference |= static_cast<unsigned int>(left[index] ^ right[index]);
        return difference == 0;
    }

    [[nodiscard]] bool EqualField(const Field& left, const Field& right) noexcept
    {
        std::array<Byte, 32> packedLeft{};
        std::array<Byte, 32> packedRight{};
        PackField(packedLeft, left);
        PackField(packedRight, right);
        return EqualBytes(packedLeft, packedRight);
    }

    [[nodiscard]] int Parity(const Field& input) noexcept
    {
        std::array<Byte, 32> packed{};
        PackField(packed, input);
        return packed[0] & 1;
    }

    void UnpackField(Field& output, const std::span<const Byte, 32> input) noexcept
    {
        for (std::size_t index = 0; index < 16U; ++index)
        {
            output[index] = static_cast<Limb>(input[index * 2U]) +
                (static_cast<Limb>(input[index * 2U + 1U]) << 8);
        }
        output[15] &= 0x7fff;
    }

    void AddField(Field& output, const Field& left, const Field& right) noexcept
    {
        for (std::size_t index = 0; index < output.size(); ++index)
            output[index] = left[index] + right[index];
    }

    void SubtractField(Field& output, const Field& left, const Field& right) noexcept
    {
        for (std::size_t index = 0; index < output.size(); ++index)
            output[index] = left[index] - right[index];
    }

    void MultiplyField(Field& output, const Field& left, const Field& right) noexcept
    {
        std::array<Limb, 31> product{};
        for (std::size_t leftIndex = 0; leftIndex < 16U; ++leftIndex)
        {
            for (std::size_t rightIndex = 0; rightIndex < 16U; ++rightIndex)
                product[leftIndex + rightIndex] += left[leftIndex] * right[rightIndex];
        }
        for (std::size_t index = 0; index < 15U; ++index)
            product[index] += 38 * product[index + 16U];
        std::copy_n(product.begin(), output.size(), output.begin());
        Carry(output);
        Carry(output);
    }

    void SquareField(Field& output, const Field& input) noexcept
    {
        MultiplyField(output, input, input);
    }

    void InvertField(Field& output, const Field& input) noexcept
    {
        Field value = input;
        for (int exponent = 253; exponent >= 0; --exponent)
        {
            SquareField(value, value);
            if (exponent != 2 && exponent != 4)
                MultiplyField(value, value, input);
        }
        output = value;
    }

    void Power2523(Field& output, const Field& input) noexcept
    {
        Field value = input;
        for (int exponent = 250; exponent >= 0; --exponent)
        {
            SquareField(value, value);
            if (exponent != 1)
                MultiplyField(value, value, input);
        }
        output = value;
    }

    void AddPoints(Point& left, const Point& right) noexcept
    {
        Field a{};
        Field b{};
        Field c{};
        Field d{};
        Field temporary{};
        Field e{};
        Field f{};
        Field g{};
        Field h{};
        SubtractField(a, left[1], left[0]);
        SubtractField(temporary, right[1], right[0]);
        MultiplyField(a, a, temporary);
        AddField(b, left[0], left[1]);
        AddField(temporary, right[0], right[1]);
        MultiplyField(b, b, temporary);
        MultiplyField(c, left[3], right[3]);
        MultiplyField(c, c, kD2);
        MultiplyField(d, left[2], right[2]);
        AddField(d, d, d);
        SubtractField(e, b, a);
        SubtractField(f, d, c);
        AddField(g, d, c);
        AddField(h, b, a);
        MultiplyField(left[0], e, f);
        MultiplyField(left[1], h, g);
        MultiplyField(left[2], g, f);
        MultiplyField(left[3], e, h);
    }

    void ConditionalSwap(Point& left, Point& right, const int choice) noexcept
    {
        for (std::size_t index = 0; index < left.size(); ++index)
            ConditionalSwap(left[index], right[index], choice);
    }

    void ScalarMultiply(
        Point& output,
        Point point,
        const std::span<const Byte, 32> scalar) noexcept
    {
        output[0] = kZero;
        output[1] = kOne;
        output[2] = kOne;
        output[3] = kZero;
        for (int bitIndex = 255; bitIndex >= 0; --bitIndex)
        {
            const int choice =
                (scalar[static_cast<std::size_t>(bitIndex >> 3)] >> (bitIndex & 7)) & 1;
            ConditionalSwap(output, point, choice);
            AddPoints(point, output);
            AddPoints(output, output);
            ConditionalSwap(output, point, choice);
        }
    }

    void ScalarBase(Point& output, const std::span<const Byte, 32> scalar) noexcept
    {
        Point base{};
        base[0] = kX;
        base[1] = kY;
        base[2] = kOne;
        MultiplyField(base[3], kX, kY);
        ScalarMultiply(output, base, scalar);
    }

    void PackPoint(std::span<Byte, 32> output, const Point& point) noexcept
    {
        Field inverse{};
        Field x{};
        Field y{};
        InvertField(inverse, point[2]);
        MultiplyField(x, point[0], inverse);
        MultiplyField(y, point[1], inverse);
        PackField(output, y);
        output[31] ^= static_cast<Byte>(Parity(x) << 7);
    }

    [[nodiscard]] bool UnpackNegative(
        Point& output,
        const std::span<const Byte, 32> packed) noexcept
    {
        Field numerator{};
        Field denominator{};
        Field denominator2{};
        Field denominator4{};
        Field denominator6{};
        Field temporary{};
        Field check{};
        output[2] = kOne;
        UnpackField(output[1], packed);
        SquareField(numerator, output[1]);
        MultiplyField(denominator, numerator, kD);
        SubtractField(numerator, numerator, output[2]);
        AddField(denominator, output[2], denominator);
        SquareField(denominator2, denominator);
        SquareField(denominator4, denominator2);
        MultiplyField(denominator6, denominator4, denominator2);
        MultiplyField(temporary, denominator6, numerator);
        MultiplyField(temporary, temporary, denominator);
        Power2523(temporary, temporary);
        MultiplyField(temporary, temporary, numerator);
        MultiplyField(temporary, temporary, denominator);
        MultiplyField(temporary, temporary, denominator);
        MultiplyField(output[0], temporary, denominator);
        SquareField(check, output[0]);
        MultiplyField(check, check, denominator);
        if (!EqualField(check, numerator))
            MultiplyField(output[0], output[0], kI);
        SquareField(check, output[0]);
        MultiplyField(check, check, denominator);
        if (!EqualField(check, numerator))
            return false;
        if (Parity(output[0]) == (packed[31] >> 7))
            SubtractField(output[0], kZero, output[0]);
        MultiplyField(output[3], output[0], output[1]);
        return true;
    }

    void ReduceModOrder(std::array<Byte, 64>& value) noexcept
    {
        std::array<Limb, 64> work{};
        for (std::size_t index = 0; index < work.size(); ++index)
            work[index] = value[index];
        value.fill(0);
        for (int index = 63; index >= 32; --index)
        {
            Limb carry = 0;
            int target = index - 32;
            for (; target < index - 12; ++target)
            {
                work[static_cast<std::size_t>(target)] += carry -
                    16 * work[static_cast<std::size_t>(index)] *
                    kOrder[static_cast<std::size_t>(target - (index - 32))];
                carry = (work[static_cast<std::size_t>(target)] + 128) >> 8;
                work[static_cast<std::size_t>(target)] -= carry << 8;
            }
            work[static_cast<std::size_t>(target)] += carry;
            work[static_cast<std::size_t>(index)] = 0;
        }
        Limb carry = 0;
        for (std::size_t index = 0; index < 32U; ++index)
        {
            work[index] += carry - (work[31] >> 4) * kOrder[index];
            carry = work[index] >> 8;
            work[index] &= 255;
        }
        for (std::size_t index = 0; index < 32U; ++index)
            work[index] -= carry * kOrder[index];
        for (std::size_t index = 0; index < 32U; ++index)
        {
            work[index + 1U] += work[index] >> 8;
            value[index] = static_cast<Byte>(work[index] & 255);
        }
        SecureZeroMemory(work.data(), sizeof(work));
    }

    [[nodiscard]] bool CanonicalScalar(const std::span<const Byte, 32> scalar) noexcept
    {
        for (int index = 31; index >= 0; --index)
        {
            const Limb value = scalar[static_cast<std::size_t>(index)];
            const Limb order = kOrder[static_cast<std::size_t>(index)];
            if (value < order)
                return true;
            if (value > order)
                return false;
        }
        return false;
    }
}

bool StrictRoster::VerifyEd25519(
    const std::span<const std::uint8_t> publicKey,
    const std::string_view message,
    const std::span<const std::uint8_t> signature) noexcept
{
    if (publicKey.size() != 32U || signature.size() != 64U ||
        message.size() > 48U * 1024U)
    {
        return false;
    }
    const std::span<const Byte, 32> key(publicKey.data(), 32U);
    const std::span<const Byte, 32> encodedR(signature.data(), 32U);
    const std::span<const Byte, 32> scalar(signature.data() + 32U, 32U);
    if (!CanonicalScalar(scalar))
        return false;

    Point publicPoint{};
    if (!UnpackNegative(publicPoint, key))
        return false;

    std::vector<Byte> hashInput;
    try
    {
        hashInput.reserve(64U + message.size());
        hashInput.insert(hashInput.end(), encodedR.begin(), encodedR.end());
        hashInput.insert(hashInput.end(), key.begin(), key.end());
        hashInput.insert(hashInput.end(), message.begin(), message.end());
    }
    catch (...)
    {
        return false;
    }
    std::array<Byte, 64> challenge{};
    if (!Sha512(hashInput, challenge))
        return false;
    ReduceModOrder(challenge);

    Point calculated{};
    ScalarMultiply(
        calculated, publicPoint,
        std::span<const Byte, 32>(challenge.data(), 32U));
    Point signaturePoint{};
    ScalarBase(signaturePoint, scalar);
    AddPoints(calculated, signaturePoint);
    std::array<Byte, 32> packed{};
    PackPoint(packed, calculated);
    const bool verified = EqualBytes(packed, encodedR);
    SecureZeroMemory(challenge.data(), challenge.size());
    if (!hashInput.empty())
        SecureZeroMemory(hashInput.data(), hashInput.size());
    return verified;
}
