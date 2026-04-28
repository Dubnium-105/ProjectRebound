using System.IO;
using System.IO.Pipes;

namespace ProjectRebound.Browser.Services;

/// <summary>
/// Windows named pipe client for the CommandFramework protocol (cmd\tjson\n).
/// Communicates with the Payload DLL injected into the game process.
/// </summary>
public sealed class PipeClient : IDisposable
{
    private NamedPipeClientStream? _stream;
    private StreamReader? _reader;
    private StreamWriter? _writer;

    public bool IsConnected => _stream is not null && _stream.IsConnected;

    public async Task<bool> ConnectAsync(string pipeName, CancellationToken ct = default)
    {
        if (_stream is not null)
            throw new InvalidOperationException("Already connected.");

        _stream = new NamedPipeClientStream(".", pipeName, PipeDirection.InOut, PipeOptions.Asynchronous);
        try
        {
            await _stream.ConnectAsync(TimeSpan.FromSeconds(10), ct);
        }
        catch (TimeoutException)
        {
            _stream.Dispose();
            _stream = null;
            return false;
        }

        _reader = new StreamReader(_stream);
        _writer = new StreamWriter(_stream) { AutoFlush = true, NewLine = "\n" };
        return true;
    }

    /// <summary>
    /// Send a command and read the response line.
    /// Response format is expected to be: cmd\tjson\n
    /// The tab-separated JSON payload is extracted and returned.
    /// </summary>
    public async Task<string> SendCommandAsync(string cmd, string jsonArgs)
    {
        if (_writer is null || _reader is null)
            throw new InvalidOperationException("Not connected.");

        await _writer.WriteAsync($"{cmd}\t{jsonArgs}\n");

        var line = await _reader.ReadLineAsync();
        if (string.IsNullOrEmpty(line))
            return "{}";

        // Response format: response_cmd\t{json}\n — extract JSON part
        var tabIndex = line.IndexOf('\t');
        return tabIndex >= 0 ? line[(tabIndex + 1)..] : line;
    }

    public void Disconnect()
    {
        try { _reader?.Dispose(); } catch { }
        try { _writer?.Dispose(); } catch { }
        try { _stream?.Dispose(); } catch { }
        _reader = null;
        _writer = null;
        _stream = null;
    }

    public void Dispose() => Disconnect();
}
