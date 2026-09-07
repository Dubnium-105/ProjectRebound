#include "../ClientLogic/NativeLoginGrantPolicy.h"

#include <algorithm>
#include <cstdlib>
#include <array>
#include <cstdint>
#include <iostream>
#include <string>
#include <vector>

namespace
{
    void Expect(const bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAIL: " << message << '\n';
            std::exit(1);
        }
    }
}

int main()
{
    constexpr std::string_view grant = "eyJhbGciOiJFZERTQSJ9.eyJqdGkiOiJ4In0.signature";
    Expect(NativeLoginGrantPolicy::ValidateGrantTokenShape(grant),
        "a three-segment base64url grant should pass the transport shape gate");
    Expect(!NativeLoginGrantPolicy::ValidateGrantTokenShape("missing-segments"),
        "a grant without JWT separators must be rejected");
    Expect(!NativeLoginGrantPolicy::ValidateGrantTokenShape("a..c"),
        "an empty JWT segment must be rejected");
    Expect(!NativeLoginGrantPolicy::ValidateGrantTokenShape("a.b.c="),
        "padding and other non-base64url transport characters must be rejected");
    Expect(!NativeLoginGrantPolicy::ValidateGrantTokenShape("a.b.c?password"),
        "URL delimiters must not enter the grant value");

    std::wstring source = L"/Game/Maps/Test?Name=Value#fragment";
    source.push_back(L'\0');
    std::wstring output;
    Expect(NativeLoginGrantPolicy::BuildUrlWithGrant(source, grant, output),
        "a bounded URL should receive the grant before its fragment");
    const std::wstring expected =
        L"/Game/Maps/Test?Name=Value&ReboundGrant=" +
        std::wstring(grant.begin(), grant.end()) + L"#fragment";
    Expect(output.size() == expected.size() + 1U &&
        output.back() == L'\0' &&
        std::wstring_view(output.data(), output.size() - 1U) == expected,
        "the grant option should be serialized with a terminal NUL and no source mutation");
    std::wstring expectedSource = L"/Game/Maps/Test?Name=Value#fragment";
    expectedSource.push_back(L'\0');
    Expect(source == expectedSource,
        "the caller-owned URL storage must remain unchanged");
    std::wstring duplicateGrant = L"/Game/Maps/Test?ReboundGrant=old";
    duplicateGrant.push_back(L'\0');
    Expect(!NativeLoginGrantPolicy::BuildUrlWithGrant(
        duplicateGrant, grant, output),
        "a pre-existing grant option must not be duplicated on retransmission");

    const std::array<std::uint8_t, 5> ticketBytes{0U, 1U, 2U, 0xFEU, 0xFFU};
    std::string encodedTicket;
    Expect(NativeLoginGrantPolicy::EncodeSteamTicket(
        ticketBytes.data(), ticketBytes.size(), encodedTicket),
        "a bounded Steam ticket should encode as base64url");
    std::vector<std::uint8_t> decodedTicket;
    Expect(NativeLoginGrantPolicy::DecodeSteamTicket(encodedTicket, decodedTicket) &&
        decodedTicket.size() == ticketBytes.size() &&
        std::equal(decodedTicket.begin(), decodedTicket.end(), ticketBytes.begin()),
        "the Steam ticket carrier must round-trip without padding");
    std::wstring carrier;
    Expect(NativeLoginGrantPolicy::BuildUrlWithGrantAndSteamTicket(
        source, grant, encodedTicket, carrier),
        "the Grant and exact Steam ticket must share one NMT_Login carrier");
    const std::wstring carrierView(carrier.data(), carrier.size() - 1U);
    Expect(carrierView.find(L"ReboundGrant=") != std::wstring::npos &&
        carrierView.find(L"ReboundSteamTicket=") != std::wstring::npos &&
        carrier.back() == L'\0',
        "the carrier must include both options before its fragment");
    std::wstring duplicateTicket = L"/Game/Maps/Test?ReboundSteamTicket=old";
    duplicateTicket.push_back(L'\0');
    Expect(!NativeLoginGrantPolicy::BuildUrlWithGrantAndSteamTicket(
        duplicateTicket,
        grant, encodedTicket, carrier),
        "a pre-existing ticket option must not be duplicated on retransmission");
    const std::wstring nativeOptions = L"/Game/Maps/Test?Name=" + std::wstring(300, L'n') +
        L"&Token=native-session-proof&Option=preserved#native-fragment";
    std::wstring sensitiveCarrier;
    Expect(NativeLoginGrantPolicy::BuildUrlWithGrantAndSteamTicket(
        nativeOptions + L'\0', grant, encodedTicket, sensitiveCarrier),
        "long native options should accept the bounded Payload carrier");
    const auto stripped = NativeLoginGrantPolicy::StripPayloadOwnedOptions(
        std::wstring_view(sensitiveCarrier.data(), sensitiveCarrier.size() - 1U));
    Expect(stripped && *stripped == nativeOptions,
        "native PreLogin must retain every original option, nonempty Token and fragment after removing only Payload credentials");
    std::string extracted;
    Expect(NativeLoginGrantPolicy::ExtractGrantFromNativeOptions(
        std::wstring_view(sensitiveCarrier.data(), sensitiveCarrier.size() - 1U), extracted) && extracted == grant,
        "the authority must recover the exact three-segment JWT emitted by the real NMT URL encoder");
    const std::wstring nativeGrant = L"?ReboundGrant=" + std::wstring(grant.begin(), grant.end());
    Expect(NativeLoginGrantPolicy::ExtractGrantFromNativeOptions(nativeGrant, extracted) && extracted == grant,
        "a Grant in the first native URL option must retain both JWT separators");
    Expect(!NativeLoginGrantPolicy::ExtractGrantFromNativeOptions(
        nativeGrant + L"&ReboundGrant=" + std::wstring(grant.begin(), grant.end()), extracted) && extracted.empty(),
        "duplicate native Grant options must reject and clear sensitive output");
    Expect(!NativeLoginGrantPolicy::ExtractGrantFromNativeOptions(L"?ReboundGrant=a..c", extracted) && extracted.empty(),
        "an empty JWT segment cannot enter native admission");
    Expect(NativeLoginGrantPolicy::StripPayloadOwnedOptions(nativeOptions) == nativeOptions,
        "sanitizing a native URL without Payload credentials must leave it unchanged");
    Expect(!NativeLoginGrantPolicy::StripPayloadOwnedOptions(
        std::wstring(NativeLoginGrantPolicy::MaxUrlCharacters, L'x')),
        "the native options filter must reject an oversized input");
    return 0;
}
