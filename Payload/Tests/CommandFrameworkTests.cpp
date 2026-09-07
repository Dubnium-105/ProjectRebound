#include "../Communication/CommandFramework.h"

#include <Windows.h>

#include <atomic>
#include <chrono>
#include <iostream>
#include <string>
#include <string_view>
#include <vector>

namespace
{
    int failures = 0;

    void Expect(const bool condition, const char* const description)
    {
        if (condition)
            return;
        ++failures;
        std::cerr << "FAILED: " << description << '\n';
    }

    class TestHandle
    {
    public:
        explicit TestHandle(const HANDLE handle = INVALID_HANDLE_VALUE) noexcept
            : handle_(handle)
        {
        }

        ~TestHandle()
        {
            if (handle_ != nullptr && handle_ != INVALID_HANDLE_VALUE)
                CloseHandle(handle_);
        }

        TestHandle(const TestHandle&) = delete;
        TestHandle& operator=(const TestHandle&) = delete;

        [[nodiscard]] HANDLE Get() const noexcept
        {
            return handle_;
        }

    private:
        HANDLE handle_;
    };

    std::string UniquePipeName(const std::string_view suffix)
    {
        return "ProjectRebound_CommandFrameworkTests_" +
            std::to_string(GetCurrentProcessId()) + "_" +
            std::to_string(GetTickCount64()) + "_" + std::string(suffix);
    }

    std::wstring PipePath(const std::string& pipeName)
    {
        return L"\\\\.\\pipe\\" + std::wstring(pipeName.begin(), pipeName.end());
    }

    TestHandle ConnectClient(const std::string& pipeName)
    {
        const std::wstring path = PipePath(pipeName);
        const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(3);

        do
        {
            const HANDLE handle = CreateFileW(
                path.c_str(),
                GENERIC_READ | GENERIC_WRITE,
                0,
                nullptr,
                OPEN_EXISTING,
                0,
                nullptr);
            if (handle != INVALID_HANDLE_VALUE)
                return TestHandle(handle);

            Sleep(10);
        } while (std::chrono::steady_clock::now() < deadline);

        return TestHandle();
    }

    bool WriteFrame(const HANDLE pipe, const std::string_view frame)
    {
        DWORD bytesWritten = 0;
        return WriteFile(
            pipe,
            frame.data(),
            static_cast<DWORD>(frame.size()),
            &bytesWritten,
            nullptr) != FALSE && bytesWritten == frame.size();
    }

    std::string ReadFrame(const HANDLE pipe)
    {
        const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(3);
        DWORD availableBytes = 0;

        while (std::chrono::steady_clock::now() < deadline)
        {
            if (!PeekNamedPipe(pipe, nullptr, 0, nullptr, &availableBytes, nullptr))
                return {};
            if (availableBytes != 0)
                break;
            Sleep(10);
        }

        if (availableBytes == 0)
            return {};

        std::vector<char> buffer(availableBytes);
        DWORD bytesRead = 0;
        if (!ReadFile(
            pipe,
            buffer.data(),
            static_cast<DWORD>(buffer.size()),
            &bytesRead,
            nullptr))
        {
            return {};
        }
        return std::string(buffer.data(), bytesRead);
    }

    void TestInvalidConfiguration()
    {
        CommandFramework framework;
        framework.SetPipeName("invalid\\name");
        Expect(!framework.Start(), "unsafe pipe name is rejected");
        Expect(!framework.IsRunning(), "rejected framework remains stopped");
    }

    void TestProtocolAndRestart()
    {
        CommandFramework framework;
        const std::string pipeName = UniquePipeName("protocol");
        std::atomic<unsigned int> joinCalls{0};
        std::atomic<bool> joinRanOnListener{false};
        std::atomic<unsigned int> clearCalls{0};

        framework.SetPipeName(pipeName);
        framework.SetWatchdogTimeout(3000);
        framework.SetWriteTimeout(1000);
        framework.SetJoinCallback([&](const std::string&, const std::string&,
            const nlohmann::json& expectedScope)
            {
                Expect(expectedScope.value("attempt_id", "") == "att_join",
                    "join callback receives the frozen scope");
                joinRanOnListener.store(framework.IsListenerThread());
                const unsigned int call = ++joinCalls;
                if (call == 1)
                    return CommandFramework::JoinResult{
                        true, "accepted", "join queued", 17};
                if (call == 2)
                    return CommandFramework::JoinResult{
                        false, "busy", "a match transition is already pending"};
                return CommandFramework::JoinResult{
                    false,
                    "native_client_grant_injection_unverified",
                    "native NMT_Login Grant injection is not verified for this build"};
            });
        framework.SetServerStatusCallback([]()
            {
                return nlohmann::json{
                    {"state", "RUNNING"},
                    {"player_count", 2},
                    {"round_state", "InProgress"}
                };
            });
        framework.SetClientMatchConnectionConfirmationCallback(
            [](const nlohmann::json& arguments) {
                return nlohmann::json{
                    {"accepted", arguments.value("operation_sequence", 0) == 17},
                    {"code", "operation_scope_mismatch"},
                    {"message", "the native client operation does not match"}
                };
            });
        framework.SetMatchAllocationCallback([](const nlohmann::json& arguments)
            {
                return nlohmann::json{
                    {"accepted", arguments.value("admission_key_id", "") == "adm_1"},
                    {"code", "allocation_rejected"},
                    {"message", "test rejection"},
                    {"payload_version", "1.0.0"},
                    {"game_binary_sha256",
                        "181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843"}
                };
            });
        framework.SetMatchJoinGrantCallback([](const nlohmann::json& arguments)
            {
                return nlohmann::json{
                    {"accepted", arguments.value("join_grant", "") == "signed.join.grant"},
                    {"code", "grant_rejected"},
                    {"message", "test rejection"}
                };
            });
        framework.SetMatchAuthorityCallback([](const nlohmann::json& arguments)
            {
                return nlohmann::json{
                    {"accepted", arguments.value("transport_target", "") == "10.26.0.2:7777"},
                    {"endpoint_host", "10.26.0.2"},
                    {"endpoint_port", 7777},
                    {"world_instance_id", "world_test_1"},
                    {"native_connection_nonce", "host_nonce_0123456789abcdef"}
                };
            });
        framework.SetMatchConnectionEventsCallback([](const nlohmann::json& arguments)
            {
                const auto after = arguments.value("after_sequence", 0ULL);
                return nlohmann::json{
                    {"next_sequence", after + 1},
                    {"events", nlohmann::json::array({nlohmann::json{
                        {"sequence", after + 1},
                        {"attempt_id", "mat_test"},
                        {"route_generation", 1},
                        {"player_id", "player_test"},
                        {"grant_jti", "mj_test"},
                        {"connection_generation", 2},
                        {"state", "DISCONNECTED"}
                    }})}
                };
            });
        framework.SetMatchAdmissionReservationCallback([](const nlohmann::json& arguments)
            {
                return nlohmann::json{
                    {"accepted", true},
                    {"code", "accepted"},
                    {"status", "queued"},
                    {"native_connection_nonce", arguments.value("native_connection_nonce", "")}
                };
            });
        framework.SetMatchConnectionConfirmationCallback([](const nlohmann::json& arguments)
            {
                return nlohmann::json{
                    {"accepted", true},
                    {"code", "accepted"},
                    {"status", "queued"},
                    {"native_connection_nonce", arguments.value("native_connection_nonce", "")}
                };
            });
        framework.SetMatchAdmissionReleaseCallback([](const nlohmann::json& arguments)
            {
                return nlohmann::json{
                    {"accepted", true},
                    {"code", "accepted"},
                    {"status", "queued"},
                    {"native_connection_nonce", arguments.value("native_connection_nonce", "")}
                };
            });
        framework.SetMatchClearResultCallback([&clearCalls](
            const nlohmann::json& arguments)
            {
                const bool scoped = arguments.value("attempt_id", "") == "att_clear" &&
                    arguments.value("authority_session_id", "") == "auth_clear" &&
                    arguments.value("world_instance_id", "world_clear") == "world_clear" &&
                    arguments.value("roster_revision", 0) == 4 &&
                    arguments.value("route_generation", 0) == 2;
                if (!scoped)
                {
                    return nlohmann::json{
                        {"accepted", false},
                        {"code", "clear_scope_required"},
                        {"message", "test clear scope is required"},
                        {"native_cleared", false}
                    };
                }
                const int call = ++clearCalls;
                if (call == 1)
                {
                    return nlohmann::json{
                        {"accepted", false},
                        {"code", "cleanup_pending"},
                        {"status", "cleanup_pending"},
                        {"message", "test native teardown is pending"},
                        {"native_cleared", false},
                        {"attempt_id", "att_clear"},
                        {"authority_session_id", "auth_clear"},
                        {"world_instance_id", "world_clear"},
                        {"roster_revision", 4},
                        {"route_generation", 2},
                        {"world_teardown_required", true}
                    };
                }
                return nlohmann::json{
                    {"accepted", true},
                    {"code", "cleared"},
                    {"status", "cleared"},
                    {"native_cleared", true},
                    {"attempt_id", "att_clear"},
                    {"authority_session_id", "auth_clear"},
                    {"world_instance_id", "world_clear"},
                    {"roster_revision", 4},
                    {"route_generation", 2},
                    {"world_teardown_required", false}
                };
            });

        Expect(framework.Start(), "framework starts");
        Expect(framework.IsRunning(), "framework reports running");

        TestHandle client = ConnectClient(pipeName);
        Expect(client.Get() != INVALID_HANDLE_VALUE, "same-user client connects");
        if (client.Get() != INVALID_HANDLE_VALUE)
        {
            Expect(WriteFrame(client.Get(), "ping\t{\"request_id\":\"ping-1\"}\n"),
                "ping request is written");
            Expect(ReadFrame(client.Get()) ==
                "pong\t{\"request_id\":\"ping-1\"}\n",
                "pong echoes request id");

            Expect(WriteFrame(client.Get(),
                "join\t{\"ip\":\"127.0.0.1:7777\",\"token\":\"signed.join.grant\",\"expected_scope\":{\"attempt_id\":\"att_join\"},\"request_id\":\"join-1\"}\n"),
                "join request is written");
            const std::string joined = ReadFrame(client.Get());
            Expect(joined.find("join_ack\t") == 0 &&
                joined.find("\"operation_sequence\":17") != std::string::npos,
                "accepted join receives its actual native operation sequence");
            Expect(joinRanOnListener.load(), "join callback runs on listener thread");

            Expect(WriteFrame(client.Get(),
                "join\t{\"ip\":\"127.0.0.1:7777\",\"token\":\"signed.join.grant-2\",\"expected_scope\":{\"attempt_id\":\"att_join\"},\"request_id\":\"join-2\"}\n"),
                "second join request is written");
            const std::string busy = ReadFrame(client.Get());
            Expect(busy.find("error\t") == 0 &&
                busy.find("\"code\":\"busy\"") != std::string::npos &&
                busy.find("\"request_id\":\"join-2\"") != std::string::npos,
                "rejected join receives correlated busy error");

            Expect(WriteFrame(client.Get(),
                "join\t{\"ip\":\"127.0.0.1:7777\",\"request_id\":\"join-empty-token\"}\n"),
                "empty-token join request is written");
            const std::string emptyToken = ReadFrame(client.Get());
            Expect(emptyToken.find("\"code\":\"native_admission_required\"") != std::string::npos,
                "empty-token online join is rejected before callback");

            Expect(WriteFrame(client.Get(),
                "join\t{\"ip\":\"127.0.0.1:0\",\"request_id\":\"join-invalid-target\"}\n"),
                "invalid join request is written");
            Expect(ReadFrame(client.Get()).find("\"code\":\"invalid_target\"") !=
                std::string::npos,
                "invalid target is rejected");

            Expect(WriteFrame(client.Get(),
                "join\t{\"ip\":\"127.0.0.1:7777\",\"token\":\"signed.join.grant-3\",\"expected_scope\":{\"attempt_id\":\"att_join\"},\"request_id\":\"join-unverified\"}\n"),
                "unverified join request is written");
            const std::string unverified = ReadFrame(client.Get());
            Expect(unverified.find("\"code\":\"native_client_grant_injection_unverified\"") !=
                std::string::npos &&
                unverified.find("\"request_id\":\"join-unverified\"") !=
                std::string::npos,
                "unverified native injection is not reported as busy");

            Expect(WriteFrame(client.Get(),
                "join\t{\"ip\":\"127.0.0.1:7777\",\"token\":\"signed.join.grant\"}\n"),
                "uncorrelated join request is written");
            const std::string missingJoinRequestId = ReadFrame(client.Get());
            Expect(missingJoinRequestId.find("error\t") == 0 &&
                missingJoinRequestId.find("\"code\":\"invalid_request\"") != std::string::npos &&
                missingJoinRequestId.find("\"request_id\":") == std::string::npos,
                "join without request id is rejected before native admission");

            Expect(WriteFrame(client.Get(),
                "join\t{\"ip\":\"127.0.0.1:7777\",\"token\":\"signed.join.grant\",\"request_id\":\"join-no-scope\"}\n"),
                "join without scope request is written");
            const std::string missingScope = ReadFrame(client.Get());
            Expect(missingScope.find("\"code\":\"scope_required\"") != std::string::npos &&
                joinCalls.load() == 3,
                "join without scope never calls the native transition handler");

            Expect(WriteFrame(client.Get(),
                "confirm_client_match_connection\t{\"operation_sequence\":17,\"request_id\":\"client-confirm-1\"}\n"),
                "client confirmation request is written");
            const std::string clientConfirmed = ReadFrame(client.Get());
            Expect(clientConfirmed.find("confirm_client_match_connection_ack\t") == 0 &&
                clientConfirmed.find("\"request_id\":\"client-confirm-1\"") != std::string::npos,
                "client connection confirmation uses the client callback and correlated ACK");
            Expect(WriteFrame(client.Get(),
                "confirm_client_match_connection\t{\"operation_sequence\":16,\"request_id\":\"client-confirm-stale\"}\n"),
                "stale client confirmation request is written");
            const std::string staleClientConfirmation = ReadFrame(client.Get());
            Expect(staleClientConfirmation.find("\"code\":\"operation_scope_mismatch\"") != std::string::npos &&
                staleClientConfirmation.find("\"request_id\":\"client-confirm-stale\"") != std::string::npos,
                "client confirmation preserves native scoped rejection");

            Expect(WriteFrame(client.Get(),
                "server_status\t{\"request_id\":\"status-1\"}\n"),
                "server status request is written");
            const std::string status = ReadFrame(client.Get());
            Expect(status.find("server_status_ack\t") == 0 &&
                status.find("\"player_count\":2") != std::string::npos &&
                status.find("\"request_id\":\"status-1\"") != std::string::npos,
                "server status response is correlated");

            Expect(WriteFrame(client.Get(),
                "install_match_allocation\t{\"request_id\":\"allocation-1\",\"allocation\":\"signed.jwt.value\",\"admission_key_id\":\"adm_1\",\"admission_public_key_base64\":\"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"}\n"),
                "allocation request is written");
            const std::string allocation = ReadFrame(client.Get());
            Expect(allocation.find("install_match_allocation_ack\t") == 0 &&
                allocation.find("\"request_id\":\"allocation-1\"") != std::string::npos &&
                allocation.find("signed.jwt.value") == std::string::npos,
                "allocation acknowledgement is correlated and does not echo secrets");

            Expect(WriteFrame(client.Get(),
                "install_match_join_grant\t{\"request_id\":\"grant-1\",\"join_grant\":\"signed.join.grant\"}\n"),
                "join grant request is written");
            const std::string staged = ReadFrame(client.Get());
            Expect(staged.find("install_match_join_grant_ack\t") == 0 &&
                staged.find("\"request_id\":\"grant-1\"") != std::string::npos &&
                staged.find("signed.join.grant") == std::string::npos,
                "join grant acknowledgement is correlated and does not echo secrets");

            Expect(WriteFrame(client.Get(),
                "start_match_authority\t{\"request_id\":\"authority-1\",\"transport_target\":\"10.26.0.2:7777\"}\n"),
                "authority request is written");
            const std::string authority = ReadFrame(client.Get());
            Expect(authority.find("start_match_authority_ack\t") == 0 &&
                authority.find("\"endpoint_port\":7777") != std::string::npos &&
                authority.find("\"native_connection_nonce\":\"host_nonce_0123456789abcdef\"") != std::string::npos &&
                authority.find("\"request_id\":\"authority-1\"") != std::string::npos,
                "authority acknowledgement is correlated and public-only");

            Expect(WriteFrame(client.Get(),
                "match_connection_events\t{\"request_id\":\"events-1\",\"after_sequence\":7}\n"),
                "connection event request is written");
            const std::string events = ReadFrame(client.Get());
            Expect(events.find("match_connection_events_ack\t") == 0 &&
                events.find("\"request_id\":\"events-1\"") != std::string::npos &&
                events.find("\"next_sequence\":8") != std::string::npos &&
                events.find("\"state\":\"DISCONNECTED\"") != std::string::npos,
                "connection event response is correlated and generation-scoped");

            const std::string scopedReceipt =
                "{\"request_id\":\"receipt-1\",\"attempt_id\":\"att_test\","
                "\"authority_session_id\":\"auth_test\",\"world_instance_id\":\"world_test\","
                "\"roster_revision\":1,\"route_generation\":1,\"player_id\":\"p_test\","
                "\"grant_jti\":\"mj_test\",\"native_connection_nonce\":"
                "\"0123456789abcdef0123456789abcdef\",\"connection_generation\":1}";
            Expect(WriteFrame(client.Get(),
                "confirm_match_admission\t" + scopedReceipt + "\n"),
                "backend reservation receipt is written");
            const std::string reserved = ReadFrame(client.Get());
            Expect(reserved.find("confirm_match_admission_ack\t") == 0 &&
                reserved.find("\"request_id\":\"receipt-1\"") != std::string::npos &&
                reserved.find("\"native_connection_nonce\":\"0123456789abcdef0123456789abcdef\"") != std::string::npos,
                "backend reservation receipt is correlated");

            Expect(WriteFrame(client.Get(),
                "confirm_match_connection\t" + scopedReceipt + "\n"),
                "backend connection receipt is written");
            const std::string confirmed = ReadFrame(client.Get());
            Expect(confirmed.find("confirm_match_connection_ack\t") == 0 &&
                confirmed.find("\"request_id\":\"receipt-1\"") != std::string::npos &&
                confirmed.find("\"native_connection_nonce\":\"0123456789abcdef0123456789abcdef\"") != std::string::npos,
                "backend connection receipt is correlated");

            Expect(WriteFrame(client.Get(),
                "release_match_admission\t" + scopedReceipt + "\n"),
                "backend release receipt is written");
            const std::string released = ReadFrame(client.Get());
            Expect(released.find("release_match_admission_ack\t") == 0 &&
                released.find("\"request_id\":\"receipt-1\"") != std::string::npos &&
                released.find("\"native_connection_nonce\":\"0123456789abcdef0123456789abcdef\"") != std::string::npos,
                "backend release receipt is correlated");

            const std::string clearScope =
                "{\"attempt_id\":\"att_clear\",\"authority_session_id\":\"auth_clear\","
                "\"world_instance_id\":\"world_clear\",\"roster_revision\":4,"
                "\"route_generation\":2";
            Expect(WriteFrame(client.Get(),
                "clear_match_allocation\t" + clearScope +
                    ",\"request_id\":\"clear-1\"}\n"),
                "scoped allocation clear request is written");
            const std::string pending = ReadFrame(client.Get());
            Expect(pending.find("clear_match_allocation_pending\t") == 0 &&
                pending.find("\"request_id\":\"clear-1\"") != std::string::npos &&
                pending.find("\"native_cleared\":false") != std::string::npos,
                "allocation clear reports pending native teardown");
            Expect(WriteFrame(client.Get(),
                "clear_match_allocation\t" + clearScope +
                    ",\"request_id\":\"clear-2\"}\n"),
                "scoped allocation clear retry is written");
            const std::string cleared = ReadFrame(client.Get());
            Expect(cleared.find("clear_match_allocation_ack\t") == 0 &&
                cleared.find("\"request_id\":\"clear-2\"") != std::string::npos &&
                cleared.find("\"native_cleared\":true") != std::string::npos &&
                clearCalls.load() == 2,
                "allocation clear acknowledgement is correlated after teardown");
        }

        framework.Stop();
        Expect(!framework.IsRunning(), "framework stops");

        Expect(framework.Start(), "framework restarts after a complete stop");
        framework.Stop();
    }

    void TestStopWhileWaitingForClient()
    {
        CommandFramework framework;
        framework.SetPipeName(UniquePipeName("stop"));
        Expect(framework.Start(), "waiting framework starts");

        const auto startedAt = std::chrono::steady_clock::now();
        framework.Stop();
        const auto elapsed = std::chrono::steady_clock::now() - startedAt;
        Expect(elapsed < std::chrono::seconds(2),
            "stop cancels pending ConnectNamedPipe promptly");
    }
}

int main()
{
    TestInvalidConfiguration();
    TestProtocolAndRestart();
    TestStopWhileWaitingForClient();

    if (failures != 0)
    {
        std::cerr << failures << " test(s) failed\n";
        return 1;
    }

    std::cout << "CommandFramework tests passed\n";
    return 0;
}
