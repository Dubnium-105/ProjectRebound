#include "CommandFramework.h"

#include <Aclapi.h>

#include <algorithm>
#include <array>
#include <cstdint>
#include <exception>
#include <utility>

#pragma comment(lib, "Advapi32.lib")

namespace
{
    constexpr DWORD RetryDelayMs = 1000;
    constexpr unsigned int MaxProtocolErrorsPerConnection = 3;
    constexpr std::size_t MaxPipeNameBytes = 200;

    void SecureClear(std::string& value) noexcept
    {
        volatile char* data = value.empty() ? nullptr : value.data();
        for (std::size_t index = 0; data && index < value.size(); ++index)
            data[index] = 0;
        value.clear();
    }

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

void CommandFramework::SetServerStatusCallback(ServerStatusCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onServerStatus = std::move(callback);
    }
}

void CommandFramework::SetMatchAllocationCallback(MatchAllocationCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchAllocation = std::move(callback);
    }
}

void CommandFramework::SetMatchJoinGrantCallback(MatchJoinGrantCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchJoinGrant = std::move(callback);
    }
}

void CommandFramework::SetMatchAuthorityCallback(MatchAuthorityCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchAuthority = std::move(callback);
    }
}

void CommandFramework::SetMatchConnectionEventsCallback(
    MatchConnectionEventsCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchConnectionEvents = std::move(callback);
    }
}

void CommandFramework::SetMatchClearCallback(MatchClearCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchClear = std::move(callback);
    }
}

void CommandFramework::SetMatchClearResultCallback(MatchClearResultCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchClearResult = std::move(callback);
    }
}

void CommandFramework::SetPayloadStatusCallback(PayloadStatusCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onPayloadStatus = std::move(callback);
    }
}

void CommandFramework::SetMatchCancelCallback(MatchCancelCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchCancel = std::move(callback);
    }
}

void CommandFramework::SetMatchAdmissionReservationCallback(
    MatchAdmissionReceiptCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchAdmissionReservation = std::move(callback);
    }
}

void CommandFramework::SetMatchConnectionConfirmationCallback(
    MatchAdmissionReceiptCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchConnectionConfirmation = std::move(callback);
    }
}

void CommandFramework::SetMatchAdmissionReleaseCallback(
    MatchAdmissionReceiptCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onMatchAdmissionRelease = std::move(callback);
    }
}

void CommandFramework::SetClientMatchConnectionConfirmationCallback(
    MatchAdmissionReceiptCallback callback)
{
    std::lock_guard<std::mutex> lock(lifecycleMutex);
    if (!running.load() && !stopping)
    {
        std::lock_guard<std::mutex> callbackLock(callbackMutex);
        onClientMatchConnectionConfirmation = std::move(callback);
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
            // Join transitions are asynchronous and can outlive the pipe
            // write that started them.  Require the correlation key before
            // validating or invoking the native join callback so the caller
            // can never receive an uncorrelated admission result.
            if (!request.requestId.has_value())
            {
                return SendError("invalid_request", "request_id is required")
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
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

            // Online joins are always identity-bound. An empty token used to
            // fall through to the direct-open client path, which made a pipe
            // request an unrostered online entry point. Offline/PvE launchers
            // use their isolated command-line path and do not use this join
            // command.
            if (token.empty())
            {
                return SendError(
                    "native_admission_required",
                    "online join requires a signed short-lived grant",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }

            const auto expectedScope = request.arguments.find("expected_scope");
            if (expectedScope == request.arguments.end() ||
                !expectedScope->is_object() || expectedScope->empty())
            {
                SecureClear(token);
                return SendError("scope_required",
                    "online join requires its frozen expected scope", request.requestId)
                    ? FrameResult::Processed : FrameResult::TransportError;
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
            const JoinResult result = joinCallback(target, token, *expectedScope);
            SecureClear(token);
            if (!result.accepted)
            {
                const std::string code = result.code.empty() ?
                    "busy" : result.code;
                const std::string message = result.message.empty()
                    ? "a match transition is already pending"
                    : result.message;
                return SendError(code, message, request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }

            if (result.operationSequence == 0)
            {
                return SendError("operation_sequence_unavailable",
                    "native join did not return its operation sequence", request.requestId)
                    ? FrameResult::Processed : FrameResult::TransportError;
            }
            return SendResponse(
                "join_ack",
                CommandProtocol::WithRequestId(
                    nlohmann::json{{"status", "accepted"},
                        {"operation_sequence", result.operationSequence}},
                    request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "install_match_allocation")
        {
            if (!request.requestId.has_value())
            {
                return SendError("invalid_request", "request_id is required")
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            const auto allocation = request.arguments.find("allocation");
            const auto keyId = request.arguments.find("admission_key_id");
            const auto publicKey = request.arguments.find("admission_public_key_base64");
            if (allocation == request.arguments.end() || !allocation->is_string() ||
                keyId == request.arguments.end() || !keyId->is_string() ||
                publicKey == request.arguments.end() || !publicKey->is_string() ||
                allocation->get_ref<const std::string&>().empty() ||
                allocation->get_ref<const std::string&>().size() > 48U * 1024U ||
                keyId->get_ref<const std::string&>().empty() ||
                keyId->get_ref<const std::string&>().size() > 128U ||
                publicKey->get_ref<const std::string&>().empty() ||
                publicKey->get_ref<const std::string&>().size() > 256U)
            {
                return SendError(
                    "invalid_request", "match allocation fields are invalid", request.requestId)
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            MatchAllocationCallback callback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                callback = onMatchAllocation;
            }
            if (!callback)
            {
                return SendError(
                    "admission_unavailable", "strict admission handler is unavailable",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            const nlohmann::json result = callback(request.arguments);
            if (!result.value("accepted", false))
            {
                return SendError(
                    result.value("code", "allocation_rejected"),
                    result.value("message", "Payload rejected the match allocation"),
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            return SendResponse(
                "install_match_allocation_ack",
                CommandProtocol::WithRequestId(
                    nlohmann::json{
                        {"status", "accepted"},
                        {"payload_version", result.value("payload_version", "")},
                        {"game_binary_sha256", result.value("game_binary_sha256", "")}
                    },
                    request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "install_match_join_grant")
        {
            if (!request.requestId.has_value())
            {
                return SendError("invalid_request", "request_id is required")
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            const auto grant = request.arguments.find("join_grant");
            if (grant == request.arguments.end() || !grant->is_string() ||
                grant->get_ref<const std::string&>().empty() ||
                grant->get_ref<const std::string&>().size() > CommandProtocol::MaxTokenBytes)
            {
                return SendError(
                    "invalid_request", "join grant is missing or too large", request.requestId)
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            MatchJoinGrantCallback callback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                callback = onMatchJoinGrant;
            }
            if (!callback)
            {
                return SendError(
                    "admission_unavailable", "strict join grant handler is unavailable",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            const nlohmann::json result = callback(request.arguments);
            if (!result.value("accepted", false))
            {
                return SendError(
                    result.value("code", "grant_rejected"),
                    result.value("message", "Payload rejected the join grant"),
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            return SendResponse(
                "install_match_join_grant_ack",
                CommandProtocol::WithRequestId(
                    nlohmann::json{{"status", "staged"}}, request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "start_match_authority")
        {
            if (!request.requestId.has_value())
            {
                return SendError("invalid_request", "request_id is required")
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            const auto target = request.arguments.find("transport_target");
            std::string targetError;
            if (target == request.arguments.end() || !target->is_string() ||
                !CommandProtocol::ValidateMatchTarget(
                    target->get_ref<const std::string&>(), &targetError))
            {
                return SendError(
                    "invalid_target", targetError.empty() ? "transport target is invalid" : targetError,
                    request.requestId)
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            MatchAuthorityCallback callback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                callback = onMatchAuthority;
            }
            if (!callback)
            {
                return SendError(
                    "admission_unavailable", "strict authority handler is unavailable",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            const nlohmann::json result = callback(request.arguments);
            if (!result.value("accepted", false))
            {
                return SendError(
                    result.value("code", "authority_rejected"),
                    result.value("message", "Payload rejected authority startup"),
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            const std::string endpointHost = result.value("endpoint_host", "");
            const int endpointPort = result.value("endpoint_port", 0);
            const std::string worldInstanceId = result.value("world_instance_id", "");
            const std::string nativeConnectionNonce =
                result.value("native_connection_nonce", "");
            const std::string endpoint = endpointPort >= 1 && endpointPort <= 65535
                ? CommandProtocol::FormatMatchTarget(
                    endpointHost, static_cast<std::uint16_t>(endpointPort))
                : std::string{};
            if (endpoint.empty() || worldInstanceId.empty() ||
                nativeConnectionNonce.empty())
            {
                return SendError(
                    endpoint.empty() ? "invalid_authority_endpoint" :
                        (worldInstanceId.empty() ? "authority_world_unverified" :
                            "native_connection_nonce_unavailable"),
                    endpoint.empty() ? "authority returned an invalid endpoint" :
                        (worldInstanceId.empty() ?
                            "authority did not return a verified world instance" :
                            "authority did not return a native connection nonce"),
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            return SendResponse(
                "start_match_authority_ack",
                CommandProtocol::WithRequestId(
                    nlohmann::json{
                        {"status", "ready"},
                        {"endpoint_host", endpointHost},
                        {"endpoint_port", endpointPort},
                        {"world_instance_id", result.value("world_instance_id", "")},
                        {"native_connection_nonce", nativeConnectionNonce},
                        {"operation_sequence", result.value("operation_sequence", 0ULL)}
                    },
                    request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "match_connection_events")
        {
            if (!request.requestId.has_value())
            {
                return SendError("invalid_request", "request_id is required")
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            const auto after = request.arguments.find("after_sequence");
            if (after == request.arguments.end() ||
                (!after->is_number_unsigned() && !after->is_number_integer()) ||
                (after->is_number_integer() && after->get<std::int64_t>() < 0))
            {
                return SendError(
                    "invalid_request", "after_sequence must be a non-negative integer",
                    request.requestId)
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            MatchConnectionEventsCallback callback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                callback = onMatchConnectionEvents;
            }
            if (!callback)
            {
                return SendError(
                    "admission_unavailable", "strict connection event handler is unavailable",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            nlohmann::json result = callback(request.arguments);
            if (!result.is_object())
            {
                return SendError(
                    "connection_events_unavailable", "strict connection event snapshot is invalid",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            return SendResponse(
                "match_connection_events_ack",
                CommandProtocol::WithRequestId(std::move(result), request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "confirm_match_admission" ||
            request.command == "confirm_match_connection" ||
            request.command == "confirm_client_match_connection" ||
            request.command == "release_match_admission")
        {
            if (!request.requestId.has_value())
            {
                return SendError("invalid_request", "request_id is required")
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            MatchAdmissionReceiptCallback callback;
            std::string responseCommand;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                if (request.command == "confirm_match_admission")
                {
                    callback = onMatchAdmissionReservation;
                    responseCommand = "confirm_match_admission_ack";
                }
                else if (request.command == "confirm_match_connection")
                {
                    callback = onMatchConnectionConfirmation;
                    responseCommand = "confirm_match_connection_ack";
                }
                else if (request.command == "confirm_client_match_connection")
                {
                    callback = onClientMatchConnectionConfirmation;
                    responseCommand = "confirm_client_match_connection_ack";
                }
                else
                {
                    callback = onMatchAdmissionRelease;
                    responseCommand = "release_match_admission_ack";
                }
            }
            if (!callback)
            {
                return SendError(
                    "admission_unavailable",
                    "scoped native admission receipt handler is unavailable",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            const nlohmann::json result = callback(request.arguments);
            if (!result.value("accepted", false))
            {
                return SendError(
                    result.value("code", "admission_receipt_rejected"),
                    result.value("message", "Payload rejected the scoped admission receipt"),
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            return SendResponse(
                responseCommand,
                CommandProtocol::WithRequestId(result, request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "clear_match_allocation")
        {
            if (!request.requestId.has_value())
            {
                return SendError("invalid_request", "request_id is required")
                    ? FrameResult::ProtocolError
                    : FrameResult::TransportError;
            }
            MatchClearCallback callback;
            MatchClearResultCallback resultCallback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                callback = onMatchClear;
                resultCallback = onMatchClearResult;
            }
            if (!callback && !resultCallback)
            {
                return SendError(
                    "admission_unavailable", "strict admission clear handler is unavailable",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            nlohmann::json result = resultCallback
                ? resultCallback(request.arguments)
                : (callback(), nlohmann::json{{"accepted", true}, {"code", "cleared"}});
            if (!result.value("accepted", false))
            {
                const nlohmann::json pending = CommandProtocol::WithRequestId(
                    nlohmann::json{
                        {"code", result.value("code", "cleanup_pending")},
                        {"message", result.value("message", "native match cleanup is still pending")},
                        {"status", result.value("status", "cleanup_pending")},
                        {"native_cleared", result.value("native_cleared", false)},
                        {"attempt_id", result.value("attempt_id", "")},
                        {"authority_session_id", result.value("authority_session_id", "")},
                        {"world_instance_id", result.value("world_instance_id", "")},
                        {"roster_revision", result.value("roster_revision", 0)},
                        {"route_generation", result.value("route_generation", 0)},
                        {"world_teardown_required", result.value("world_teardown_required", true)},
                        {"native_teardown_mode", result.value("native_teardown_mode", "not_requested")},
                        {"process_exit_manager_required", result.value("process_exit_manager_required", false)}
                    }, request.requestId);
                return SendResponse(
                    "clear_match_allocation_pending", pending)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            return SendResponse(
                "clear_match_allocation_ack",
                CommandProtocol::WithRequestId(
                    nlohmann::json{
                        {"code", result.value("code", "cleared")},
                        {"message", result.value("message", "")},
                        {"status", result.value("status", "cleared")},
                        {"native_cleared", result.value("native_cleared", true)},
                        {"attempt_id", result.value("attempt_id", "")},
                        {"authority_session_id", result.value("authority_session_id", "")},
                        {"world_instance_id", result.value("world_instance_id", "")},
                        {"roster_revision", result.value("roster_revision", 0)},
                        {"route_generation", result.value("route_generation", 0)},
                        {"world_teardown_required", result.value("world_teardown_required", false)},
                        {"native_teardown_mode", result.value("native_teardown_mode", "world_return_to_menu")},
                        {"process_exit_manager_required", result.value("process_exit_manager_required", false)}
                    }, request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "cancel_match_transition")
        {
            MatchCancelCallback callback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                callback = onMatchCancel;
            }
            if (!callback)
            {
                return SendError(
                    "unavailable", "match cancellation is not available",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            const nlohmann::json result = callback();
            if (!result.value("accepted", false))
            {
                return SendError(
                    result.value("code", "cancel_rejected"),
                    result.value("message", "match transition could not be cancelled"),
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            return SendResponse(
                "cancel_match_transition_ack",
                CommandProtocol::WithRequestId(result, request.requestId))
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

        if (request.command == "server_status")
        {
            ServerStatusCallback statusCallback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                statusCallback = onServerStatus;
            }
            if (!statusCallback)
            {
                return SendError(
                    "unavailable",
                    "server status handler is not available",
                    request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }

            return SendResponse(
                "server_status_ack",
                CommandProtocol::WithRequestId(statusCallback(), request.requestId))
                ? FrameResult::Processed
                : FrameResult::TransportError;
        }

        if (request.command == "payload_status")
        {
            PayloadStatusCallback callback;
            {
                std::lock_guard<std::mutex> callbackLock(callbackMutex);
                callback = onPayloadStatus;
            }
            if (!callback)
            {
                return SendError(
                    "unavailable", "payload status is not available", request.requestId)
                    ? FrameResult::Processed
                    : FrameResult::TransportError;
            }
            return SendResponse(
                "payload_status_ack",
                CommandProtocol::WithRequestId(callback(), request.requestId))
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
