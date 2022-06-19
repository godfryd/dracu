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
    sh "#{GO} build -v"
  end
end

task :helloworld => :build do
  sh "src/dracu ubuntu echo 'hello world'"
end
