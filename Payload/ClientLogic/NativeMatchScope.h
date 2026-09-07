#pragma once

#include "../Libs/json.hpp"

#include <cstdint>
#include <limits>
#include <optional>
#include <string>
#include <string_view>

// The client never treats the opaque Join Grant as locally trusted claims.
// Toolbox supplies this frozen, non-secret correlation scope over the guarded
// pipe, and the native authority/backend confirmation must echo the same
// values before the local client may report Playable.
struct NativeMatchScope
{
    std::string attemptId;
    std::string authoritySessionId;
    std::string worldInstanceId;
    std::int64_t rosterRevision = 0;
    int routeGeneration = 0;
    std::string playerId;
    std::string grantJti;
    int connectionGeneration = 0;

    [[nodiscard]] bool IsValid(std::string* error = nullptr) const noexcept;
    [[nodiscard]] bool Matches(const NativeMatchScope& other) const noexcept;
    [[nodiscard]] nlohmann::json ToJson() const;

    [[nodiscard]] static std::optional<NativeMatchScope> FromJson(
        const nlohmann::json& value,
        std::string* error = nullptr) noexcept;

    // A P2P HOST owns the local native socket and therefore has no remote
    // Join Grant JTI.  Keep that exception explicit and role-tagged; the
    // ordinary MEMBER parser remains strict and requires a non-empty JTI.
    [[nodiscard]] bool IsHostValid(std::string* error = nullptr) const noexcept;
    [[nodiscard]] static std::optional<NativeMatchScope> FromHostJson(
        const nlohmann::json& value,
        std::string* error = nullptr) noexcept;
};

struct NativeClientMatchConfirmation
{
    NativeMatchScope scope;
    std::string nativeConnectionNonce;
    std::uint64_t operationSequence = 0;
    bool hostScope = false;

    [[nodiscard]] bool IsValid(std::string* error = nullptr) const noexcept;
    [[nodiscard]] nlohmann::json ToJson() const;

    [[nodiscard]] static std::optional<NativeClientMatchConfirmation> FromJson(
        const nlohmann::json& value,
        std::string* error = nullptr) noexcept;
};

namespace NativeMatchScopeDetail
{
    inline void SetError(std::string* error, const char* value) noexcept
    {
        if (error)
            *error = value ? value : "invalid native match scope";
    }

    inline bool IsBoundedText(const std::string& value) noexcept
    {
        if (value.empty() || value.size() > 256U)
            return false;
        for (const unsigned char ch : value)
        {
            // Scope values are correlation identifiers, never free-form text.
            // Reject controls and whitespace so a visually empty value cannot
            // pass the exact-scope gate or alter line-oriented diagnostics.
            if (ch <= 0x20U || ch == 0x7fU)
                return false;
        }
        return true;
    }

    inline bool IsSafeNonce(const std::string& value) noexcept
    {
        if (value.size() < 16U || value.size() > 128U)
            return false;
        for (const unsigned char ch : value)
        {
            const bool alphaNumeric =
                (ch >= 'A' && ch <= 'Z') ||
                (ch >= 'a' && ch <= 'z') ||
                (ch >= '0' && ch <= '9');
            if (!alphaNumeric && ch != '.' && ch != '_' && ch != ':' && ch != '-')
                return false;
        }
        return true;
    }

    inline bool ReadString(
        const nlohmann::json& value,
        const char* name,
        std::string& output,
        std::string* error)
    {
        const auto it = value.find(name);
        if (it == value.end() || !it->is_string())
        {
            SetError(error, "native match scope string field is missing or invalid");
            return false;
        }
        output = it->get<std::string>();
        if (!IsBoundedText(output))
        {
            SetError(error, "native match scope string field is empty or too large");
            return false;
        }
        return true;
    }

    inline bool ReadInt64(
        const nlohmann::json& value,
        const char* name,
        std::int64_t& output,
        std::string* error)
    {
        const auto it = value.find(name);
        if (it == value.end() || !it->is_number_integer())
        {
            SetError(error, "native match scope integer field is missing or invalid");
            return false;
        }
        output = it->get<std::int64_t>();
        return true;
    }

    inline bool ReadInt(
        const nlohmann::json& value,
        const char* name,
        int& output,
        std::string* error)
    {
        std::int64_t value64 = 0;
        if (!ReadInt64(value, name, value64, error) ||
            value64 < (std::numeric_limits<int>::min)() ||
            value64 > (std::numeric_limits<int>::max)())
        {
            SetError(error, "native match scope integer field is out of range");
            return false;
        }
        output = static_cast<int>(value64);
        return true;
    }
}

inline bool NativeMatchScope::IsValid(std::string* error) const noexcept
{
    if (!NativeMatchScopeDetail::IsBoundedText(attemptId) ||
        !NativeMatchScopeDetail::IsBoundedText(authoritySessionId) ||
        !NativeMatchScopeDetail::IsBoundedText(worldInstanceId) ||
        !NativeMatchScopeDetail::IsBoundedText(playerId) ||
        !NativeMatchScopeDetail::IsBoundedText(grantJti))
    {
        NativeMatchScopeDetail::SetError(
            error, "native match scope contains an empty or oversized identity");
        return false;
    }
    if (rosterRevision < 1 || routeGeneration < 1 || connectionGeneration < 1)
    {
        NativeMatchScopeDetail::SetError(
            error, "native match scope generation values must be positive");
        return false;
    }
    return true;
}

inline bool NativeMatchScope::IsHostValid(std::string* error) const noexcept
{
    if (!NativeMatchScopeDetail::IsBoundedText(attemptId) ||
        !NativeMatchScopeDetail::IsBoundedText(authoritySessionId) ||
        !NativeMatchScopeDetail::IsBoundedText(worldInstanceId) ||
        !NativeMatchScopeDetail::IsBoundedText(playerId) ||
        !grantJti.empty() || grantJti.size() > 256U)
    {
        NativeMatchScopeDetail::SetError(
            error, "native HOST scope must have an empty grant JTI and valid identity");
        return false;
    }
    if (rosterRevision < 1 || routeGeneration < 1 || connectionGeneration < 1)
    {
        NativeMatchScopeDetail::SetError(
            error, "native HOST scope generation values must be positive");
        return false;
    }
    return true;
}

inline bool NativeMatchScope::Matches(const NativeMatchScope& other) const noexcept
{
    return attemptId == other.attemptId &&
        authoritySessionId == other.authoritySessionId &&
        worldInstanceId == other.worldInstanceId &&
        rosterRevision == other.rosterRevision &&
        routeGeneration == other.routeGeneration &&
        playerId == other.playerId &&
        grantJti == other.grantJti &&
        connectionGeneration == other.connectionGeneration;
}

inline nlohmann::json NativeMatchScope::ToJson() const
{
    return nlohmann::json{
        {"attempt_id", attemptId},
        {"authority_session_id", authoritySessionId},
        {"world_instance_id", worldInstanceId},
        {"roster_revision", rosterRevision},
        {"route_generation", routeGeneration},
        {"player_id", playerId},
        {"grant_jti", grantJti},
        {"connection_generation", connectionGeneration}
    };
}

inline std::optional<NativeMatchScope> NativeMatchScope::FromJson(
    const nlohmann::json& value,
    std::string* error) noexcept
{
    try
    {
        if (!value.is_object())
        {
            NativeMatchScopeDetail::SetError(
                error, "native match scope must be an object");
            return std::nullopt;
        }
        NativeMatchScope result;
        if (!NativeMatchScopeDetail::ReadString(
                value, "attempt_id", result.attemptId, error) ||
            !NativeMatchScopeDetail::ReadString(
                value, "authority_session_id", result.authoritySessionId, error) ||
            !NativeMatchScopeDetail::ReadString(
                value, "world_instance_id", result.worldInstanceId, error) ||
            !NativeMatchScopeDetail::ReadInt64(
                value, "roster_revision", result.rosterRevision, error) ||
            !NativeMatchScopeDetail::ReadInt(
                value, "route_generation", result.routeGeneration, error) ||
            !NativeMatchScopeDetail::ReadString(
                value, "player_id", result.playerId, error) ||
            !NativeMatchScopeDetail::ReadString(
                value, "grant_jti", result.grantJti, error) ||
            !NativeMatchScopeDetail::ReadInt(
                value, "connection_generation", result.connectionGeneration, error) ||
            !result.IsValid(error))
        {
            return std::nullopt;
        }
        return result;
    }
    catch (...)
    {
        NativeMatchScopeDetail::SetError(
            error, "native match scope parsing failed");
        return std::nullopt;
    }
}

inline std::optional<NativeMatchScope> NativeMatchScope::FromHostJson(
    const nlohmann::json& value,
    std::string* error) noexcept
{
    try
    {
        if (!value.is_object())
        {
            NativeMatchScopeDetail::SetError(
                error, "native HOST match scope must be an object");
            return std::nullopt;
        }
        const auto role = value.find("room_role");
        if (role == value.end() || !role->is_string() ||
            role->get<std::string>() != "HOST")
        {
            NativeMatchScopeDetail::SetError(
                error, "native HOST match scope must declare room_role HOST");
            return std::nullopt;
        }
        NativeMatchScope result;
        if (!NativeMatchScopeDetail::ReadString(
                value, "attempt_id", result.attemptId, error) ||
            !NativeMatchScopeDetail::ReadString(
                value, "authority_session_id", result.authoritySessionId, error) ||
            !NativeMatchScopeDetail::ReadString(
                value, "world_instance_id", result.worldInstanceId, error) ||
            !NativeMatchScopeDetail::ReadInt64(
                value, "roster_revision", result.rosterRevision, error) ||
            !NativeMatchScopeDetail::ReadInt(
                value, "route_generation", result.routeGeneration, error) ||
            !NativeMatchScopeDetail::ReadString(
                value, "player_id", result.playerId, error) ||
            !NativeMatchScopeDetail::ReadInt(
                value, "connection_generation", result.connectionGeneration, error))
        {
            return std::nullopt;
        }
        const auto grant = value.find("grant_jti");
        if (grant == value.end() || !grant->is_string() ||
            !grant->get<std::string>().empty())
        {
            NativeMatchScopeDetail::SetError(
                error, "native HOST match scope grant JTI must be empty");
            return std::nullopt;
        }
        result.grantJti.clear();
        if (!result.IsHostValid(error))
            return std::nullopt;
        return result;
    }
    catch (...)
    {
        NativeMatchScopeDetail::SetError(
            error, "native HOST match scope parsing failed");
        return std::nullopt;
    }
}

inline bool NativeClientMatchConfirmation::IsValid(std::string* error) const noexcept
{
    if (hostScope ? !scope.IsHostValid(error) : !scope.IsValid(error))
        return false;
    if (!NativeMatchScopeDetail::IsSafeNonce(nativeConnectionNonce))
    {
        NativeMatchScopeDetail::SetError(
            error, "native connection nonce is missing or invalid");
        return false;
    }
    if (operationSequence == 0)
    {
        NativeMatchScopeDetail::SetError(
            error, "native client confirmation operation sequence is missing");
        return false;
    }
    return true;
}

inline nlohmann::json NativeClientMatchConfirmation::ToJson() const
{
    nlohmann::json result = scope.ToJson();
    if (hostScope)
        result["room_role"] = "HOST";
    result["native_connection_nonce"] = nativeConnectionNonce;
    result["operation_sequence"] = operationSequence;
    return result;
}

inline std::optional<NativeClientMatchConfirmation>
NativeClientMatchConfirmation::FromJson(
    const nlohmann::json& value,
    std::string* error) noexcept
{
    try
    {
        if (value.contains("scope"))
        {
            NativeMatchScopeDetail::SetError(
                error, "native client confirmation scope must be flat");
            return std::nullopt;
        }
        const auto role = value.find("room_role");
        bool hostScope = false;
        if (role != value.end())
        {
            if (!role->is_string())
            {
                NativeMatchScopeDetail::SetError(
                    error, "native client confirmation room role is invalid");
                return std::nullopt;
            }
            const std::string roleValue = role->get<std::string>();
            if (roleValue != "HOST" && roleValue != "MEMBER")
            {
                NativeMatchScopeDetail::SetError(
                    error, "native client confirmation room role is invalid");
                return std::nullopt;
            }
            hostScope = roleValue == "HOST";
        }
        const auto scope = hostScope
            ? NativeMatchScope::FromHostJson(value, error)
            : NativeMatchScope::FromJson(value, error);
        if (!scope)
            return std::nullopt;
        const auto nonce = value.find("native_connection_nonce");
        const auto sequence = value.find("operation_sequence");
        if (nonce == value.end() || !nonce->is_string() ||
            sequence == value.end() || !sequence->is_number_integer())
        {
            NativeMatchScopeDetail::SetError(
                error, "native client confirmation fields are missing or invalid");
            return std::nullopt;
        }
        NativeClientMatchConfirmation result;
        result.scope = *scope;
        result.hostScope = hostScope;
        result.nativeConnectionNonce = nonce->get<std::string>();
        const auto sequenceValue = sequence->get<std::int64_t>();
        if (sequenceValue <= 0)
        {
            NativeMatchScopeDetail::SetError(
                error, "native client confirmation operation sequence is invalid");
            return std::nullopt;
        }
        result.operationSequence = static_cast<std::uint64_t>(sequenceValue);
        if (!result.IsValid(error))
            return std::nullopt;
        return result;
    }
    catch (...)
    {
        NativeMatchScopeDetail::SetError(
            error, "native client confirmation parsing failed");
        return std::nullopt;
    }
}
