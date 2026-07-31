#include "CommandFramework.h"

#include <Aclapi.h>

#include <algorithm>
#include <array>
#include <exception>
#include <utility>

#pragma comment(lib, "Advapi32.lib")

namespace
{
    constexpr DWORD RetryDelayMs = 1000;
    constexpr unsigned int MaxProtocolErrorsPerConnection = 3;
    constexpr std::size_t MaxPipeNameBytes = 200;

    bool IsSafePipeNameCharacter(const unsigned char ch) noexcept
    {
        return (ch >= 'a' && ch <= 'z') ||
            (ch >= 'A' && ch <= 'Z') ||
            (ch >= '0' && ch <= '9') ||
            ch == '_' || ch == '-' || ch == '.';
    }

    class CurrentPipeRegistration
    {
    public:
        CurrentPipeRegistration(
            HANDLE& publishedPipe,
            std::mutex& mutex,
            const HANDLE pipe)
            : publishedPipe_(publishedPipe)
            , mutex_(mutex)
            , pipe_(pipe)
        {
            std::lock_guard<std::mutex> lock(mutex_);
            publishedPipe_ = pipe_;
        }

        ~CurrentPipeRegistration()
        {
            std::lock_guard<std::mutex> lock(mutex_);
            if (publishedPipe_ == pipe_)
                publishedPipe_ = INVALID_HANDLE_VALUE;
        }

        CurrentPipeRegistration(const CurrentPipeRegistration&) = delete;
        CurrentPipeRegistration& operator=(const CurrentPipeRegistration&) = delete;

    private:
        HANDLE& publishedPipe_;
        std::mutex& mutex_;
        HANDLE pipe_;
    };
}

CommandFramework::CommandFramework() = default;

CommandFramework::~CommandFramework()
{
    Stop();
    ReleaseSecurity();
}

void CommandFramework::SetPipeName(const std::string& name)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
        pipeName = name;
}

void CommandFramework::SetWatchdogTimeout(const DWORD timeoutMs)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
        watchdogTimeoutMs = timeoutMs;
}

void CommandFramework::SetWriteTimeout(const DWORD timeoutMs)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
        writeTimeoutMs = timeoutMs;
}

void CommandFramework::SetJoinCallback(JoinCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onJoin = std::move(callback);
    }
}

void CommandFramework::SetLogCallback(LogCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onLog = std::move(callback);
    }
}

void CommandFramework::SetDebugCallback(DebugCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onDebug = std::move(callback);
    }
}

bool CommandFramework::BuildPipePath(std::string& failureReason)
{
    if (pipeName.empty() || pipeName.size() > MaxPipeNameBytes)
    {
        failureReason = "pipe name is empty or too long";
        return false;
    }
    if (!std::all_of(pipeName.begin(), pipeName.end(), [](const unsigned char ch)
        {
            return IsSafePipeNameCharacter(ch);
        }))
    {
        failureReason = "pipe name contains unsupported characters";
        return false;
    }

    pipePath = LR"(\\.\pipe\)";
    pipePath.append(pipeName.begin(), pipeName.end());
    if (pipePath.size() >= 256)
    {
        failureReason = "pipe path is too long";
        return false;
    }
    return true;
}

bool CommandFramework::InitializeSecurity(std::string& failureReason)
{
    if (securityInitialized)
        return true;

    UniqueHandle processToken;
    HANDLE rawToken = nullptr;
    if (!OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &rawToken))
    {
        failureReason = "OpenProcessToken failed: " + std::to_string(GetLastError());
        return false;
    }
    processToken.Reset(rawToken);

    DWORD tokenInfoBytes = 0;
    GetTokenInformation(processToken.Get(), TokenUser, nullptr, 0, &tokenInfoBytes);
    if (GetLastError() != ERROR_INSUFFICIENT_BUFFER || tokenInfoBytes == 0)
    {
        failureReason = "GetTokenInformation(size) failed: " + std::to_string(GetLastError());
        return false;
    }

    std::vector<unsigned char> tokenInfo(tokenInfoBytes);
    if (!GetTokenInformation(
        processToken.Get(),
        TokenUser,
        tokenInfo.data(),
        tokenInfoBytes,
        &tokenInfoBytes))
    {
        failureReason = "GetTokenInformation(TokenUser) failed: " + std::to_string(GetLastError());
        return false;
    }

    const auto* const tokenUser = reinterpret_cast<const TOKEN_USER*>(tokenInfo.data());
    const DWORD sidBytes = GetLengthSid(tokenUser->User.Sid);
    if (sidBytes == 0)
    {
        failureReason = "GetLengthSid failed: " + std::to_string(GetLastError());
        return false;
    }

    allowedUserSid.resize(sidBytes);
    if (!CopySid(sidBytes, allowedUserSid.data(), tokenUser->User.Sid))
    {
        failureReason = "CopySid failed: " + std::to_string(GetLastError());
        allowedUserSid.clear();
        return false;
    }

    EXPLICIT_ACCESSW access{};
    access.grfAccessPermissions = GENERIC_READ | GENERIC_WRITE;
    access.grfAccessMode = SET_ACCESS;
    access.grfInheritance = NO_INHERITANCE;
    BuildTrusteeWithSidW(&access.Trustee, allowedUserSid.data());

    const DWORD aclError = SetEntriesInAclW(1, &access, nullptr, &pipeAcl);
    if (aclError != ERROR_SUCCESS)
    {
        failureReason = "SetEntriesInAclW failed: " + std::to_string(aclError);
        allowedUserSid.clear();
        return false;
    }

    if (!InitializeSecurityDescriptor(&securityDescriptor, SECURITY_DESCRIPTOR_REVISION))
    {
        failureReason = "InitializeSecurityDescriptor failed: " + std::to_string(GetLastError());
        ReleaseSecurity();
        return false;
    }
    if (!SetSecurityDescriptorDacl(&securityDescriptor, TRUE, pipeAcl, FALSE))
    {
        failureReason = "SetSecurityDescriptorDacl failed: " + std::to_string(GetLastError());
        ReleaseSecurity();
        return false;
    }

    securityAttributes.nLength = sizeof(securityAttributes);
    securityAttributes.lpSecurityDescriptor = &securityDescriptor;
    securityAttributes.bInheritHandle = FALSE;
    securityInitialized = true;
    return true;
}

void CommandFramework::ReleaseSecurity() noexcept
{
    if (pipeAcl != nullptr)
    {
        LocalFree(pipeAcl);
        pipeAcl = nullptr;
    }
    allowedUserSid.clear();
    securityInitialized = false;
    securityAttributes = {};
    securityDescriptor = {};
}

bool CommandFramework::Start()
{
    std::unique_lock<std::mutex> lock(lifecycleMutex);
    if (running.load() || stopping || listenerThread.joinable())
        return false;

    std::string failureReason;
    if (!BuildPipePath(failureReason) || !InitializeSecurity(failureReason))
    {
        lock.unlock();
        Log("[CMDFW] Cannot start: " + failureReason + ".");
        return false;
    }

    stopEvent.Reset(CreateEventW(nullptr, TRUE, FALSE, nullptr));
    if (!stopEvent.IsValid())
    {
        const DWORD error = GetLastError();
        lock.unlock();
        LogWin32Error("CreateEventW(stop)", error);
        return false;
    }

    connectionFaulted.store(false);
    running.store(true);
    try
    {
        listenerThread = std::thread(&CommandFramework::ListenerLoop, this);
    }
    catch (const std::exception& exception)
    {
        running.store(false);
        stopEvent.Reset();
        lock.unlock();
        Log(std::string("[CMDFW] Failed to create listener thread: ") + exception.what());
        return false;
    }
    catch (...)
    {
        running.store(false);
        stopEvent.Reset();
        lock.unlock();
        Log("[CMDFW] Failed to create listener thread.");
        return false;
    }

    const std::string startedPipeName = pipeName;
    lock.unlock();
    Log("[CMDFW] Started on pipe: \\\\.\\pipe\\" + startedPipeName);
    return true;
}

void CommandFramework::Stop() noexcept
{
    std::unique_lock<std::mutex> lifecycleLock(lifecycleMutex);
    if (stopping)
        return;

    const bool wasRunning = running.exchange(false);

    if (stopEvent.IsValid())
        SetEvent(stopEvent.Get());

    {
        std::lock_guard<std::mutex> writeLock(writeMutex);
        if (hCurrentPipe != INVALID_HANDLE_VALUE)
            CancelIoEx(hCurrentPipe, nullptr);
    }

    if (listenerThread.joinable())
    {
        if (listenerThread.get_id() == std::this_thread::get_id())
        {
            lifecycleLock.unlock();
            Log("[CMDFW] Stop requested on listener thread; owner must join it.");
            return;
        }

        stopping = true;
        std::thread threadToJoin = std::move(listenerThread);
        lifecycleLock.unlock();
        threadToJoin.join();
        lifecycleLock.lock();
    }

    {
        std::lock_guard<std::mutex> writeLock(writeMutex);
        hCurrentPipe = INVALID_HANDLE_VALUE;
    }
    stopEvent.Reset();
    stopping = false;
    lifecycleLock.unlock();

    if (wasRunning)
        Log("[CMDFW] Stopped.");
}

bool CommandFramework::IsRunning() const noexcept
{
    return running.load();
}

bool CommandFramework::IsListenerThread() const noexcept
{
    const DWORD listenerId = listenerThreadId.load();
    return listenerId != 0 && listenerId == GetCurrentThreadId();
}

bool CommandFramework::IsAuthorizedClient(const HANDLE pipe) const
{
    ULONG clientPid = 0;
    if (!GetNamedPipeClientProcessId(pipe, &clientPid) || clientPid == 0)
    {
        LogWin32Error("GetNamedPipeClientProcessId", GetLastError());
        return false;
    }

    DWORD serverSession = 0;
    DWORD clientSession = 0;
    if (!ProcessIdToSessionId(GetCurrentProcessId(), &serverSession) ||
        !ProcessIdToSessionId(clientPid, &clientSession) ||
        serverSession != clientSession)
    {
        Log("[CMDFW] Rejected client from a different or unknown Windows session.");
        return false;
    }

    UniqueHandle clientProcess(OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, FALSE, clientPid));
    if (!clientProcess.IsValid())
    {
        LogWin32Error("OpenProcess(pipe client)", GetLastError());
        return false;
    }

    HANDLE rawToken = nullptr;
    if (!OpenProcessToken(clientProcess.Get(), TOKEN_QUERY, &rawToken))
    {
        LogWin32Error("OpenProcessToken(pipe client)", GetLastError());
        return false;
    }
    UniqueHandle clientToken(rawToken);

    DWORD tokenInfoBytes = 0;
    GetTokenInformation(clientToken.Get(), TokenUser, nullptr, 0, &tokenInfoBytes);
    if (GetLastError() != ERROR_INSUFFICIENT_BUFFER || tokenInfoBytes == 0)
    {
        LogWin32Error("GetTokenInformation(client size)", GetLastError());
        return false;
    }

    std::vector<unsigned char> tokenInfo(tokenInfoBytes);
    if (!GetTokenInformation(
        clientToken.Get(),
        TokenUser,
        tokenInfo.data(),
        tokenInfoBytes,
        &tokenInfoBytes))
    {
        LogWin32Error("GetTokenInformation(client user)", GetLastError());
        return false;
    }

    const auto* const tokenUser = reinterpret_cast<const TOKEN_USER*>(tokenInfo.data());
    return !allowedUserSid.empty() &&
        EqualSid(
            reinterpret_cast<PSID>(const_cast<unsigned char*>(allowedUserSid.data())),
            tokenUser->User.Sid) != FALSE;
}

CommandFramework::IoResult CommandFramework::CompleteIo(
    const HANDLE handle,
    OVERLAPPED& operation) const noexcept
{
    IoResult result;
    if (GetOverlappedResult(handle, &operation, &result.bytesTransferred, FALSE))
    {
        result.status = IoStatus::Completed;
        result.error = ERROR_SUCCESS;
        return result;
    }

    result.error = GetLastError();
    result.status = result.error == ERROR_MORE_DATA ? IoStatus::Completed : IoStatus::Failed;
    return result;
}

void CommandFramework::CancelAndDrain(
    const HANDLE handle,
    OVERLAPPED& operation) const noexcept
{
    // The OVERLAPPED structure and its buffers must remain alive until the
    // operation reaches a terminal state, even when CancelIoEx reports that
    // completion won the race (ERROR_NOT_FOUND).
    CancelIoEx(handle, &operation);
    DWORD ignoredBytes = 0;
    GetOverlappedResult(handle, &operation, &ignoredBytes, TRUE);
}

CommandFramework::IoResult CommandFramework::WaitForPendingIo(
    const HANDLE handle,
    OVERLAPPED& operation,
    const DWORD timeoutMs) const noexcept
{
    const std::array<HANDLE, 2> waitHandles{stopEvent.Get(), operation.hEvent};
    const DWORD waitResult = WaitForMultipleObjects(
        static_cast<DWORD>(waitHandles.size()),
        waitHandles.data(),
        FALSE,
        timeoutMs == 0 ? INFINITE : timeoutMs);

    if (waitResult == WAIT_OBJECT_0 + 1)
        return CompleteIo(handle, operation);

    IoResult result;
    if (waitResult == WAIT_OBJECT_0)
    {
        result.status = IoStatus::Stopped;
        result.error = ERROR_OPERATION_ABORTED;
    }
    else if (waitResult == WAIT_TIMEOUT)
    {
        result.status = IoStatus::TimedOut;
        result.error = WAIT_TIMEOUT;
    }
    else
    {
        result.status = IoStatus::Failed;
        result.error = GetLastError();
    }

    CancelAndDrain(handle, operation);
    return result;
}

void CommandFramework::ListenerLoop() noexcept
{
    listenerThreadId.store(GetCurrentThreadId());
    try
    {
        while (running.load())
        {
            UniqueHandle pipe(CreateNamedPipeW(
                pipePath.c_str(),
                PIPE_ACCESS_DUPLEX | FILE_FLAG_OVERLAPPED | FILE_FLAG_FIRST_PIPE_INSTANCE,
                PIPE_TYPE_MESSAGE | PIPE_READMODE_MESSAGE | PIPE_WAIT | PIPE_REJECT_REMOTE_CLIENTS,
                1,
                4096,
                4096,
                0,
                &securityAttributes));

            if (!pipe.IsValid())
            {
                LogWin32Error("CreateNamedPipeW", GetLastError());
                if (WaitForSingleObject(stopEvent.Get(), RetryDelayMs) == WAIT_OBJECT_0)
                    break;
                continue;
            }

            UniqueHandle connectEvent(CreateEventW(nullptr, TRUE, FALSE, nullptr));
            if (!connectEvent.IsValid())
            {
                LogWin32Error("CreateEventW(connect)", GetLastError());
                continue;
            }

            OVERLAPPED connectOperation{};
            connectOperation.hEvent = connectEvent.Get();
            bool connected = false;
            bool stopRequested = false;

            if (ConnectNamedPipe(pipe.Get(), &connectOperation))
            {
                connected = true;
            }
            else
            {
                const DWORD connectError = GetLastError();
                if (connectError == ERROR_PIPE_CONNECTED)
                {
                    connected = true;
                }
                else if (connectError == ERROR_IO_PENDING)
                {
                    const IoResult result = WaitForPendingIo(pipe.Get(), connectOperation, 0);
                    connected = result.status == IoStatus::Completed;
                    stopRequested = result.status == IoStatus::Stopped;
                    if (!connected && !stopRequested)
                        LogWin32Error("ConnectNamedPipe completion", result.error);
                }
                else
                {
                    LogWin32Error("ConnectNamedPipe", connectError);
                }
            }

            if (stopRequested || !running.load())
                break;
            if (!connected)
                continue;
            if (!IsAuthorizedClient(pipe.Get()))
            {
                Log("[CMDFW] Rejected unauthorized pipe client.");
                DisconnectNamedPipe(pipe.Get());
                continue;
            }

            {
                connectionFaulted.store(false);
                CurrentPipeRegistration currentPipe(hCurrentPipe, writeMutex, pipe.Get());

                Log("[CMDFW] Client connected.");
                try
                {
                    (void)ReadClient(pipe.Get());
                }
                catch (const std::exception& exception)
                {
                    OutputDebugStringA(exception.what());
                    OutputDebugStringA("\n");
                    Log("[CMDFW] Client processing failed with a C++ exception.");
                    running.store(false);
                }
                catch (...)
                {
                    Log("[CMDFW] Client processing failed.");
                    running.store(false);
                }
            }

            DisconnectNamedPipe(pipe.Get());
            Log("[CMDFW] Client disconnected.");
        }
    }
    catch (const std::exception& exception)
    {
        running.store(false);
        {
            std::lock_guard<std::mutex> writeLock(writeMutex);
            hCurrentPipe = INVALID_HANDLE_VALUE;
        }
        OutputDebugStringA(exception.what());
        OutputDebugStringA("\n");
        Log("[CMDFW] Listener failed with a C++ exception.");
    }
    catch (...)
    {
        running.store(false);
        {
            std::lock_guard<std::mutex> writeLock(writeMutex);
            hCurrentPipe = INVALID_HANDLE_VALUE;
        }
        Log("[CMDFW] Listener failed.");
    }
    listenerThreadId.store(0);
}

bool CommandFramework::ReadClient(const HANDLE pipe)
{
    std::array<char, 4096> buffer{};
    std::string lineBuffer;
    unsigned int protocolErrors = 0;

    while (running.load() && !connectionFaulted.load())
    {
        UniqueHandle readEvent(CreateEventW(nullptr, TRUE, FALSE, nullptr));
        if (!readEvent.IsValid())
        {
            LogWin32Error("CreateEventW(read)", GetLastError());
            return false;
        }

        OVERLAPPED readOperation{};
        readOperation.hEvent = readEvent.Get();
        IoResult result;

        if (ReadFile(
            pipe,
            buffer.data(),
            static_cast<DWORD>(buffer.size()),
            nullptr,
            &readOperation))
        {
            result = CompleteIo(pipe, readOperation);
        }
        else
        {
            const DWORD readError = GetLastError();
            if (readError == ERROR_IO_PENDING)
            {
                result = WaitForPendingIo(pipe, readOperation, watchdogTimeoutMs);
            }
            else if (readError == ERROR_MORE_DATA)
            {
                result = CompleteIo(pipe, readOperation);
            }
            else
            {
                result.status = IoStatus::Failed;
                result.error = readError;
            }
        }

        if (result.status == IoStatus::Stopped)
            return true;
        if (result.status == IoStatus::TimedOut)
        {
            Log("[CMDFW] Client exceeded the idle read timeout.");
            return false;
        }
        if (result.status == IoStatus::Failed)
        {
            if (result.error != ERROR_BROKEN_PIPE &&
                result.error != ERROR_NO_DATA &&
                result.error != ERROR_OPERATION_ABORTED)
            {
                LogWin32Error("ReadFile", result.error);
            }
            return false;
        }
        if (result.bytesTransferred == 0)
            return false;

        lineBuffer.append(buffer.data(), result.bytesTransferred);

        std::size_t newline = std::string::npos;
        while ((newline = lineBuffer.find(CommandProtocol::Newline)) != std::string::npos)
        {
            if (newline >= CommandProtocol::MaxFrameBytes)
            {
                (void)SendError("frame_too_large", "frame exceeds 64 KiB");
                return false;
            }

            std::string frame = lineBuffer.substr(0, newline);
            lineBuffer.erase(0, newline + 1);
            if (!frame.empty() && frame.back() == '\r')
                frame.pop_back();
            const FrameResult frameResult = ParseAndDispatch(frame);
            if (frameResult == FrameResult::TransportError)
                return false;
            if (frameResult == FrameResult::ProtocolError &&
                ++protocolErrors >= MaxProtocolErrorsPerConnection)
            {
                Log("[CMDFW] Disconnecting client after repeated protocol errors.");
                return false;
            }
        }

        if (lineBuffer.size() >= CommandProtocol::MaxFrameBytes)
        {
            (void)SendError("frame_too_large", "frame exceeds 64 KiB");
            return false;
        }
    }

    return !connectionFaulted.load();
}

bool CommandFramework::SendResponse(
    const std::string& command,
    const nlohmann::json& payload)
{
    std::string frame;
    try
    {
        frame = CommandProtocol::EncodeFrame(command, payload);
    }
    catch (const std::exception& exception)
    {
        Log(std::string("[CMDFW] Cannot encode response: ") + exception.what());
        return false;
    }
    catch (...)
    {
        Log("[CMDFW] Cannot encode response.");
        return false;
    }

    DWORD writeError = ERROR_SUCCESS;
    bool wroteFrame = false;
    {
        std::lock_guard<std::mutex> writeLock(writeMutex);
        if (hCurrentPipe == INVALID_HANDLE_VALUE)
            return false;

        UniqueHandle writeEvent(CreateEventW(nullptr, TRUE, FALSE, nullptr));
        if (!writeEvent.IsValid())
        {
            writeError = GetLastError();
        }
        else
        {
            OVERLAPPED writeOperation{};
            writeOperation.hEvent = writeEvent.Get();
            IoResult result;

            if (WriteFile(
                hCurrentPipe,
                frame.data(),
                static_cast<DWORD>(frame.size()),
                nullptr,
                &writeOperation))
            {
                result = CompleteIo(hCurrentPipe, writeOperation);
            }
            else if (const DWORD error = GetLastError(); error == ERROR_IO_PENDING)
            {
                result = WaitForPendingIo(hCurrentPipe, writeOperation, writeTimeoutMs);
            }
            else
            {
                result.status = IoStatus::Failed;
                result.error = error;
            }

            wroteFrame = result.status == IoStatus::Completed &&
                result.bytesTransferred == frame.size();
            writeError = result.error;
        }

        if (!wroteFrame)
        {
            connectionFaulted.store(true);
            CancelIoEx(hCurrentPipe, nullptr);
        }
    }

    if (!wroteFrame)
        LogWin32Error("WriteFile", writeError);
    return wroteFrame;
}

CommandFramework::FrameResult CommandFramework::ParseAndDispatch(
    const std::string& frame) noexcept
{
    try
    {
        const CommandProtocol::ParseResult parsed = CommandProtocol::ParseFrame(frame);
        if (!parsed.Succeeded())
        {
            return SendError(parsed.errorCode, parsed.errorMessage, parsed.requestId)
                ? FrameResult::ProtocolError
                : FrameResult::TransportError;
        }

        return Dispatch(*parsed.request);
    }
    catch (...)
    {
        return SendError("internal_error", "request parsing failed")
            ? FrameResult::Processed
            : FrameResult::TransportError;
    }
}

CommandFramework::FrameResult CommandFramework::Dispatch(
    const CommandProtocol::Request& request) noexcept
{
    try
    {
        if (request.command == "ping")
        {
            return SendResponse(
                "pong",
                CommandProtocol::WithRequestId(nlohmann::json::object(), request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "join")
        {
            const auto ip = request.arguments.find("ip");
            if (ip == request.arguments.end() || !ip->is_string())
            {
                return SendError("invalid_request", "join.ip must be a string", request.requestId)
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }

            const std::string target = ip->get<std::string>();
            std::string targetError;
            if (!CommandProtocol::ValidateMatchTarget(target, &targetError))
            {
                return SendError("invalid_target", targetError, request.requestId)
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }

            std::string token;
            if (const auto tokenValue = request.arguments.find("token");
                tokenValue != request.arguments.end())
            {
                if (!tokenValue->is_string())
                {
                    return SendError("invalid_request", "join.token must be a string", request.requestId)
                        ? FrameResult::ProtocolError
                        : FrameResult::TransportError;
                }
                token = tokenValue->get<std::string>();
                if (token.size() > CommandProtocol::MaxTokenBytes)
                {
                    return SendError("invalid_request", "join.token is too long", request.requestId)
                        ? FrameResult::ProtocolError
                        : FrameResult::TransportError;
                }
            }

            JoinCallback joinCallback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                joinCallback = onJoin;
            }
            if (!joinCallback)
            {
                return SendError("unavailable", "join handler is not available", request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            if (!joinCallback(target, token))
            {
                return SendError("busy", "a match transition is already pending", request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }

            return SendResponse(
                "join_ack",
                CommandProtocol::WithRequestId(
                    nlohmann::json{{"status", "accepted"}},
                    request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "debug")
        {
            DebugCallback debugCallback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                debugCallback = onDebug;
            }
            if (!debugCallback)
            {
                return SendError("unavailable", "debug handler is not available", request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }

            return SendResponse(
                "debug_ack",
                CommandProtocol::WithRequestId(debugCallback(request.arguments), request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        return SendError("unknown_command", "command is not supported", request.requestId)
            ? FrameResult::ProtocolError
            : FrameResult::TransportError;
    }
    catch (const std::exception& exception)
    {
        OutputDebugStringA(exception.what());
        OutputDebugStringA("\n");
        Log("[CMDFW] Command callback failed with a C++ exception.");
    }
    catch (...)
    {
        Log("[CMDFW] Command callback failed.");
    }

    return SendError("internal_error", "command execution failed", request.requestId)
        ? FrameResult::Processed
        : FrameResult::TransportError;
}

bool CommandFramework::SendError(
    const std::string_view code,
    const std::string_view message,
    const std::optional<std::string>& requestId)
{
    try
    {
        return SendResponse("error", CommandProtocol::MakeError(code, message, requestId));
    }
    catch (...)
    {
        Log("[CMDFW] Failed to build protocol error response.");
        return false;
    }
}

void CommandFramework::Log(const std::string& message) const noexcept
{
    try
    {
        LogCallback logCallback;
        {
            std::lock_guard<std::mutex> callbackLock(callbackMutex);
            logCallback = onLog;
        }

        if (logCallback)
            logCallback(message);
        else
            OutputDebugStringA((message + "\n").c_str());
    }
    catch (...)
    {
        OutputDebugStringA("[CMDFW] Logging callback failed.\n");
    }
}

void CommandFramework::LogWin32Error(
    const std::string& operation,
    const DWORD error) const noexcept
{
    try
    {
        Log("[CMDFW] " + operation + " failed: " + std::to_string(error));
    }
    catch (...)
    {
        OutputDebugStringA("[CMDFW] Win32 operation failed.\n");
    }
}
