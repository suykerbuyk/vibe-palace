class Vp < Formula
  desc "vibe-palace AI context engine"
  homepage "https://github.com/suykerbuyk/vibe-palace"
  license any_of: ["MIT", "Apache-2.0"]

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/suykerbuyk/vibe-palace/releases/download/v#{version}/vp_#{version}_darwin_arm64.tar.gz"
    else
      url "https://github.com/suykerbuyk/vibe-palace/releases/download/v#{version}/vp_#{version}_darwin_amd64.tar.gz"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/suykerbuyk/vibe-palace/releases/download/v#{version}/vp_#{version}_linux_arm64.tar.gz"
    else
      url "https://github.com/suykerbuyk/vibe-palace/releases/download/v#{version}/vp_#{version}_linux_amd64.tar.gz"
    end
  end

  def install
    bin.install "vp"
  end

  test do
    assert_match "vp", shell_output("#{bin}/vp version")
  end
end
