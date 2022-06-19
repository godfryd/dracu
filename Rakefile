DRACU_VER = ENV['dracu_ver'] || 'v0.0.0'

GO_VER = '1.18.3'

TOOLS_DIR = File.expand_path('tools')
GO_DIR = "#{TOOLS_DIR}/#{GO_VER}"
GO = "#{GO_DIR}/bin/go"

file GO do
  Dir.chdir(TOOLS_DIR) do
    sh "wget https://dl.google.com/go/go#{GO_VER}.linux-amd64.tar.gz -O go.tar.gz"
    sh 'tar -zxf go.tar.gz'
    sh 'rm go.tar.gz'
    sh "mv go #{GO_DIR}"
    sh "#{GO} version"
  end
end

task :build => [GO] do
  Dir.chdir('src') do
    sh "#{GO} build -v -race"
    sh "mv dracu .."
  end
end

task :helloworld => :build do
  sh "dracu ubuntu echo 'hello world'"
end

task :package => :build do
  sh "tar -zcvf dracu-#{DRACU_VER}-linux-amd64.tar.gz LICENSE README.md dracu"
end
