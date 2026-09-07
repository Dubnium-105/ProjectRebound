#pragma once

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <string>
#include <string_view>
#include <vector>

namespace NativeLoginGrantPolicy
{
    // The grant is carried through the fixed NMT_Login URL FString.  Keep the
    // transport deliberately smaller than both the command-pipe limit and the
    // pinned serializer's bounded FString read.  This is a shape gate only;
    // signature and roster claims are verified by StrictRoster::Policy on the
    // authority before the native reservation is made.
    inline constexpr std::size_t MaxGrantBytes = 4096U;
    inline constexpr std::size_t MaxSteamTicketBytes = 4096U;
    inline constexpr std::size_t MaxSteamTicketEncodedBytes = 8192U;
    inline constexpr std::size_t MaxUrlCharacters = 8192U;

    inline bool IsBase64UrlCharacter(const char value) noexcept
    {
        return (value >= 'A' && value <= 'Z') ||
            (value >= 'a' && value <= 'z') ||
            (value >= '0' && value <= '9') ||
            value == '-' || value == '_';
    }

    inline bool ValidateGrantTokenShape(const std::string_view grant) noexcept
    {
        if (grant.empty() || grant.size() > MaxGrantBytes)
            return false;

        std::size_t segmentLength = 0;
        unsigned int separators = 0;
        for (const char value : grant)
        {
            if (value == '.')
            {
                if (segmentLength == 0U || ++separators > 2U)
                    return false;
                segmentLength = 0;
                continue;
            }
            if (!IsBase64UrlCharacter(value))
                return false;
            ++segmentLength;
        }
        return separators == 2U && segmentLength != 0U;
    }

    inline bool HasReboundGrantOption(const std::wstring_view url) noexcept
    {
        constexpr std::wstring_view key = L"ReboundGrant=";
        std::size_t segmentStart = 0;
        while (segmentStart <= url.size())
        {
            if (segmentStart < url.size() && url[segmentStart] == L'?')
            {
                ++segmentStart;
                continue;
            }
            if (segmentStart < url.size() && url[segmentStart] == L'#')
                return false;
            if (url.compare(segmentStart, key.size(), key) == 0)
                return true;
            const std::size_t separator = url.find_first_of(L"&#", segmentStart);
            if (separator == std::wstring_view::npos)
                return false;
            segmentStart = separator + 1U;
        }
        return false;
    }

    inline bool HasReboundSteamTicketOption(const std::wstring_view url) noexcept
    {
        constexpr std::wstring_view key = L"ReboundSteamTicket=";
        std::size_t segmentStart = 0;
        while (segmentStart <= url.size())
        {
            if (segmentStart < url.size() && url[segmentStart] == L'?')
            {
                ++segmentStart;
                continue;
            }
            if (segmentStart < url.size() && url[segmentStart] == L'#')
                return false;
            if (url.compare(segmentStart, key.size(), key) == 0)
                return true;
            const std::size_t separator = url.find_first_of(L"&#", segmentStart);
            if (separator == std::wstring_view::npos)
                return false;
            segmentStart = separator + 1U;
        }
        return false;
    }

    inline bool IsBase64UrlValue(const std::string_view value) noexcept
    {
        if (value.empty() || value.size() > MaxSteamTicketEncodedBytes)
            return false;
        return std::all_of(value.begin(), value.end(), [](const char value) {
            return IsBase64UrlCharacter(value);
        });
    }

    inline void SecureClearString(std::string& value) noexcept
    {
        volatile char* data = value.empty() ? nullptr : value.data();
        for (std::size_t index = 0; data && index < value.size(); ++index)
            data[index] = 0;
        value.clear();
    }

    inline void SecureClearWideString(std::wstring& value) noexcept
    {
        volatile wchar_t* data = value.empty() ? nullptr : value.data();
        for (std::size_t index = 0; data && index < value.size(); ++index)
            data[index] = L'\0';
        value.clear();
    }

    inline void SecureClearBytes(std::vector<std::uint8_t>& value) noexcept
    {
        volatile std::uint8_t* data = value.empty() ? nullptr : value.data();
        for (std::size_t index = 0; data && index < value.size(); ++index)
            data[index] = 0;
        value.clear();
    }

    inline bool EncodeSteamTicket(
        const std::uint8_t* bytes,
        const std::size_t byteCount,
        std::string& output) noexcept
    {
        SecureClearString(output);
        if (!bytes || byteCount == 0U || byteCount > MaxSteamTicketBytes ||
            byteCount > (MaxSteamTicketEncodedBytes * 3U) / 4U + 2U)
        {
            return false;
        }
        constexpr char alphabet[] =
            "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
        output.reserve((byteCount * 4U + 2U) / 3U);
        std::size_t index = 0;
        while (index + 2U < byteCount)
        {
            const std::uint32_t value =
                (static_cast<std::uint32_t>(bytes[index]) << 16U) |
                (static_cast<std::uint32_t>(bytes[index + 1U]) << 8U) |
                static_cast<std::uint32_t>(bytes[index + 2U]);
            output.push_back(alphabet[(value >> 18U) & 0x3FU]);
            output.push_back(alphabet[(value >> 12U) & 0x3FU]);
            output.push_back(alphabet[(value >> 6U) & 0x3FU]);
            output.push_back(alphabet[value & 0x3FU]);
            index += 3U;
        }
        const std::size_t remaining = byteCount - index;
        if (remaining != 0U)
        {
            std::uint32_t value = static_cast<std::uint32_t>(bytes[index]) << 16U;
            output.push_back(alphabet[(value >> 18U) & 0x3FU]);
            if (remaining == 2U)
            {
                value |= static_cast<std::uint32_t>(bytes[index + 1U]) << 8U;
                output.push_back(alphabet[(value >> 12U) & 0x3FU]);
                output.push_back(alphabet[(value >> 6U) & 0x3FU]);
            }
            else
            {
                output.push_back(alphabet[(value >> 12U) & 0x3FU]);
            }
        }
        return IsBase64UrlValue(output);
    }

    inline bool DecodeSteamTicket(
        const std::string_view encoded,
        std::vector<std::uint8_t>& output) noexcept
    {
        SecureClearBytes(output);
        if (!IsBase64UrlValue(encoded) || encoded.size() % 4U == 1U)
            return false;
        auto decode = [](const char value) noexcept -> int {
            if (value >= 'A' && value <= 'Z') return value - 'A';
            if (value >= 'a' && value <= 'z') return value - 'a' + 26;
            if (value >= '0' && value <= '9') return value - '0' + 52;
            if (value == '-') return 62;
            if (value == '_') return 63;
            return -1;
        };
        output.reserve((encoded.size() * 3U) / 4U);
        std::uint32_t accumulator = 0;
        unsigned int bits = 0;
        for (const char value : encoded)
        {
            const int decoded = decode(value);
            if (decoded < 0)
            {
                SecureClearBytes(output);
                return false;
            }
            accumulator = (accumulator << 6U) | static_cast<std::uint32_t>(decoded);
            bits += 6U;
            if (bits >= 8U)
            {
                bits -= 8U;
                output.push_back(static_cast<std::uint8_t>(
                    (accumulator >> bits) & 0xFFU));
            }
        }
        if (output.empty() || output.size() > MaxSteamTicketBytes)
        {
            SecureClearBytes(output);
            return false;
        }
        return true;
    }

    // Build the exact URL FString view passed to the fixed serializer.  The
    // returned string includes its terminal NUL because the native serializer
    // consumes FString.Num rather than a C++ view.  No caller-owned storage is
    // modified or adopted by this helper.
    inline bool BuildUrlWithGrant(
        const std::wstring_view sourceWithTerminalNul,
        const std::string_view grant,
        std::wstring& output)
    {
        SecureClearWideString(output);
        if (!ValidateGrantTokenShape(grant) || sourceWithTerminalNul.empty() ||
            sourceWithTerminalNul.back() != L'\0')
        {
            return false;
        }

        const std::size_t sourceLength = sourceWithTerminalNul.size() - 1U;
        if (sourceLength > MaxUrlCharacters)
            return false;
        for (std::size_t index = 0; index < sourceLength; ++index)
        {
            if (sourceWithTerminalNul[index] == L'\0')
                return false;
        }

        const std::wstring_view source = sourceWithTerminalNul.substr(0, sourceLength);
        if (HasReboundGrantOption(source))
            return false;

        std::wstring grantWide;
        grantWide.reserve(grant.size());
        for (const char value : grant)
            grantWide.push_back(static_cast<wchar_t>(static_cast<unsigned char>(value)));

        const std::size_t fragment = source.find(L'#');
        const std::wstring_view beforeFragment = source.substr(0, fragment);
        const std::wstring_view afterFragment = fragment == std::wstring_view::npos
            ? std::wstring_view{}
            : source.substr(fragment);
        std::wstring suffix;
        if (beforeFragment.empty() || beforeFragment.find(L'?') == std::wstring_view::npos)
            suffix = L"?ReboundGrant=";
        else if (beforeFragment.back() == L'?' || beforeFragment.back() == L'&')
            suffix = L"ReboundGrant=";
        else
            suffix = L"&ReboundGrant=";

        const std::size_t resultLength = beforeFragment.size() + suffix.size() +
            grantWide.size() + afterFragment.size();
        if (resultLength + 1U > MaxUrlCharacters)
            return false;

        output.reserve(resultLength + 1U);
        output.append(beforeFragment);
        output.append(suffix);
        output.append(grantWide);
        output.append(afterFragment);
        output.push_back(L'\0');
        return true;
    }

    inline bool BuildUrlWithGrantAndSteamTicket(
        const std::wstring_view sourceWithTerminalNul,
        const std::string_view grant,
        const std::string_view encodedSteamTicket,
        std::wstring& output)
    {
        SecureClearWideString(output);
        if (!ValidateGrantTokenShape(grant) ||
            !IsBase64UrlValue(encodedSteamTicket) ||
            sourceWithTerminalNul.empty() ||
            sourceWithTerminalNul.back() != L'\0')
        {
            return false;
        }

        const std::size_t sourceLength = sourceWithTerminalNul.size() - 1U;
        if (sourceLength > MaxUrlCharacters)
            return false;
        for (std::size_t index = 0; index < sourceLength; ++index)
        {
            if (sourceWithTerminalNul[index] == L'\0')
                return false;
        }
        const std::wstring_view source = sourceWithTerminalNul.substr(0, sourceLength);
        if (HasReboundGrantOption(source) || HasReboundSteamTicketOption(source))
            return false;

        std::wstring grantWide;
        grantWide.reserve(grant.size());
        for (const char value : grant)
            grantWide.push_back(static_cast<wchar_t>(static_cast<unsigned char>(value)));
        std::wstring ticketWide;
        ticketWide.reserve(encodedSteamTicket.size());
        for (const char value : encodedSteamTicket)
            ticketWide.push_back(static_cast<wchar_t>(static_cast<unsigned char>(value)));

        const std::size_t fragment = source.find(L'#');
        const std::wstring_view beforeFragment = source.substr(0, fragment);
        const std::wstring_view afterFragment = fragment == std::wstring_view::npos
            ? std::wstring_view{}
            : source.substr(fragment);
        std::wstring suffix;
        if (beforeFragment.empty() || beforeFragment.find(L'?') == std::wstring_view::npos)
            suffix = L"?ReboundGrant=";
        else if (beforeFragment.back() == L'?' || beforeFragment.back() == L'&')
            suffix = L"ReboundGrant=";
        else
            suffix = L"&ReboundGrant=";
        suffix += grantWide;
        suffix += L"&ReboundSteamTicket=";
        suffix += ticketWide;

        const std::size_t resultLength = beforeFragment.size() + suffix.size() +
            afterFragment.size();
        if (resultLength + 1U > MaxUrlCharacters)
            return false;
        output.reserve(resultLength + 1U);
        output.append(beforeFragment);
        output.append(suffix);
        output.append(afterFragment);
        output.push_back(L'\0');
        return true;
    }
}
