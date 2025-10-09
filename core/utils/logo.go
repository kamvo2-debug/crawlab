package utils

import (
	"fmt"
	"github.com/common-nighthawk/go-figure"
)

func PrintLogoWithWelcomeInfo() {
	printLogo()
	printSystemInfo()
	printWelcomeInfo()
}

func printLogo() {
	figure.NewColorFigure("Crawlab", "slant", "blue", true).Print()
	fmt.Println()
	fmt.Println("Welcome to use Crawlab: the ultimate distributed web crawling platform for efficient, scalable data extraction.")
	fmt.Println()
}

func printSystemInfo() {
	fmt.Println("System Info:")
	fmt.Printf("- Version:    %s (%s)\n", GetEditionLabel(), GetVersion())
	fmt.Printf("- Node Type:  %s\n", GetNodeTypeLabel())
	fmt.Println()
}

func printWelcomeInfo() {
	fmt.Println("For more information, please refer to the following resources:")
	fmt.Println("- Website:        https://crawlab.cn")
	fmt.Println("- Documentation:  https://docs.crawlab.cn")
	fmt.Println("- GitHub Repo:    https://github.com/crawlab-team/crawlab")
	fmt.Println()
	if IsMaster() {
		fmt.Println("Visit the web ui at https://localhost:8080 (please be patient, it takes a while to start up)")
		fmt.Println()
	}
}
