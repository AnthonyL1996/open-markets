using System.Reflection;
using System.Runtime.InteropServices;

[assembly: AssemblyTitle("OpenMarkets")]
[assembly: AssemblyDescription("Dynamic commodity market trading mod for Cities: Skylines 1")]
[assembly: AssemblyProduct("OpenMarkets")]
[assembly: AssemblyConfiguration("")]
[assembly: AssemblyCompany("")]
[assembly: ComVisible(false)]

// AssemblyVersion wildcard => each build gets a fresh build/revision number so the game
// treats every rebuild as a new assembly (helps dev hot-reload). Do NOT also set
// AssemblyFileVersion to a wildcard; leaving it unset is intentional.
// Requires <Deterministic>false</Deterministic> in the csproj.
[assembly: AssemblyVersion("0.1.*")]
